package main

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"sync/atomic"
	"syscall"
	"time"

	"zerodesk-host/capture"
	"zerodesk-host/config"
	"zerodesk-host/encoder"
	"zerodesk-host/lan_rtc"
	"zerodesk-host/logger"
	"zerodesk-host/system_ctrl"
	"zerodesk-host/web"
)

func main() {
	trayMode := flag.Bool("tray", false, "hide console window and run in Windows system tray")
	benchFrames := flag.Int("bench-frames", 0, "capture and encode N frames on startup for benchmark telemetry")
	benchExit := flag.Bool("bench-exit", false, "exit after benchmark completes")
	flag.Parse()

	if *trayMode {
		system_ctrl.HideConsoleWindow()
	}
	capture.InitDPIAwareness()

	baseDir, err := config.ResolveBaseDir()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to resolve ZeroDesk directory: %v\n", err)
		os.Exit(1)
	}

	cfg, cfgPath, err := config.LoadOrCreate(baseDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to load config: %v\n", err)
		os.Exit(1)
	}

	logDir := filepath.Join(baseDir, "logs")
	log, err := logger.NewRollingLogger(logDir, "zerodesk-host.log", cfg.LogMaxBytes, cfg.LogMaxFiles)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to initialize logger: %v\n", err)
		os.Exit(1)
	}
	defer log.Close()

	lanIPs, primaryIP, primaryMAC := lan_rtc.GetPrimaryLANInfo(cfg.FilterVirtualNICs)
	if primaryMAC != "" && cfg.MacAddress != primaryMAC {
		cfg.MacAddress = primaryMAC
		_ = config.Save(cfgPath, cfg)
	}

	fmt.Println("============================================================")
	fmt.Println("  ZeroDesk Host (Stage 3 - Tray + NVENC Zero-Copy + Blackout)")
	fmt.Println("============================================================")
	fmt.Printf("  Root Dir      : %s\n", baseDir)
	fmt.Printf("  Config File   : %s\n", cfgPath)
	fmt.Printf("  Pairing PIN   : %s\n", cfg.PinCode)
	fmt.Printf("  Bedside URL   : http://%s:%d\n", primaryIP, cfg.HTTPPort)
	if primaryMAC != "" {
		fmt.Printf("  Primary MAC   : %s\n", primaryMAC)
	}
	for _, ip := range lanIPs {
		if ip != primaryIP {
			fmt.Printf("  Alt LAN URL   : http://%s:%d\n", ip, cfg.HTTPPort)
		}
	}
	fmt.Printf("  UDP Discovery : 0.0.0.0:%d\n", cfg.DiscoveryPort)
	fmt.Printf("  Video Target  : %d FPS @ %d kbps (H.264)\n", cfg.TargetFPS, cfg.BitrateKbps)
	fmt.Printf("  Emergency Key : Ctrl + Alt + B (Force Disable Blackout)\n")
	fmt.Println("============================================================")
	log.Infof("ZeroDesk Stage 3 started (root=%s, tray=%v, http=:%d, udp=:%d, mac=%s, fps=%d, bitrate=%dkbps, filter_virtual_nics=%v)",
		baseDir, *trayMode, cfg.HTTPPort, cfg.DiscoveryPort, primaryMAC, cfg.TargetFPS, cfg.BitrateKbps, cfg.FilterVirtualNICs)

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	var rtcSrvRef atomic.Pointer[lan_rtc.RTCServer]
	blackout, bErr := system_ctrl.NewBlackoutController(func(enabled bool) {
		log.Infof("Physical blackout curtain -> %v", enabled)
		if srv := rtcSrvRef.Load(); srv != nil {
			srv.BroadcastBlackoutState(enabled)
		}
	})
	if bErr != nil {
		log.Warnf("Blackout controller unavailable (WDA_EXCLUDEFROMCAPTURE): %v", bErr)
	} else {
		defer blackout.Close()
		log.Infof("Blackout controller initialized (WDA_EXCLUDEFROMCAPTURE active, hotkey Ctrl+Alt+B)")
	}

	idrReqCh := make(chan struct{}, 4)
	rtcSrv, err := lan_rtc.NewRTCServer(cfg, log, idrReqCh, blackout)
	if err != nil {
		log.Errorf("RTCServer init failed: %v", err)
		os.Exit(1)
	}
	rtcSrvRef.Store(rtcSrv)
	defer rtcSrv.CloseAll(context.Background())

	var benchRemaining atomic.Int32
	if *benchFrames > 0 {
		benchRemaining.Store(int32(*benchFrames))
	}

	pipelineReady := make(chan error, 1)
	go runCaptureEncodePipeline(ctx, cfg, log, rtcSrv, idrReqCh, &benchRemaining, *benchExit, cancel, pipelineReady)

	if err := <-pipelineReady; err != nil {
		log.Errorf("Capture/Encode pipeline startup failed: %v", err)
		os.Exit(1)
	}

	if *trayMode {
		tray, tErr := system_ctrl.NewTrayController(system_ctrl.TrayConfig{
			URL:    fmt.Sprintf("http://%s:%d", primaryIP, cfg.HTTPPort),
			Pin:    cfg.PinCode,
			LogDir: logDir,
			OnClearBlack: func() {
				if blackout != nil {
					_ = blackout.SetEnabled(false)
				}
			},
			OnExit: func() {
				cancel()
			},
		})
		if tErr != nil {
			log.Warnf("Tray icon init failed: %v", tErr)
		} else {
			defer tray.Close()
			log.Infof("System tray icon active")
		}
	}

	go func() {
		if err := lan_rtc.RunDiscoveryServer(ctx, cfg.DiscoveryPort, cfg.HTTPPort, cfg.FilterVirtualNICs, log); err != nil {
			log.Errorf("Discovery server exited: %v", err)
		}
	}()

	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write(web.IndexHTML)
	})
	mux.HandleFunc("/api/connect", rtcSrv.HandleConnect)
	mux.HandleFunc("/api/stats", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(rtcSrv.GetStats())
	})
	mux.HandleFunc("/api/blackout", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var req struct {
			Pin    string `json:"pin"`
			Action string `json:"action"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req); err != nil {
			http.Error(w, "invalid json", http.StatusBadRequest)
			return
		}
		if subtle.ConstantTimeCompare([]byte(req.Pin), []byte(cfg.PinCode)) != 1 {
			http.Error(w, `{"error":"invalid pin"}`, http.StatusUnauthorized)
			return
		}
		if blackout == nil {
			http.Error(w, `{"error":"blackout unsupported"}`, http.StatusServiceUnavailable)
			return
		}
		var opErr error
		switch req.Action {
		case "on":
			opErr = blackout.SetEnabled(true)
		case "off":
			opErr = blackout.SetEnabled(false)
		case "toggle":
			blackout.Toggle()
		}
		if opErr != nil {
			http.Error(w, opErr.Error(), http.StatusInternalServerError)
			return
		}
		// Give UI thread brief tick to apply visibility state
		time.Sleep(30 * time.Millisecond)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"enabled": blackout.IsEnabled()})
	})
	mux.HandleFunc("/api/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok","service":"zerodesk-host","stage":2}`))
	})

	httpSrv := &http.Server{
		Addr:              fmt.Sprintf(":%d", cfg.HTTPPort),
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}

	go func() {
		log.Infof("HTTP signaling & bedside UI listening on 0.0.0.0:%d", cfg.HTTPPort)
		if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Errorf("HTTP server error: %v", err)
			cancel()
		}
	}()

	<-ctx.Done()
	log.Infof("Shutting down ZeroDesk host...")
	if blackout != nil {
		_ = blackout.SetEnabled(false)
	}
	shutCtx, shutCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer shutCancel()
	_ = httpSrv.Shutdown(shutCtx)
}

func initBestEncoder(capturer *capture.DXGICapturer, cfg config.Config, log *logger.RollingLogger) (encoder.VideoEncoder, encoder.TextureEncoder, error) {
	nvenc := encoder.NewNVENCEncoder()
	if err := nvenc.InitD3D11(capturer.D3DDevice(), capturer.Width, capturer.Height, cfg.TargetFPS, cfg.BitrateKbps); err == nil {
		return nvenc, nvenc, nil
	} else {
		log.Warnf("NVENC D3D11 unavailable (%v), falling back to MediaFoundation H.264 MFT", err)
	}

	mf := encoder.NewMFH264Encoder()
	if err := mf.Init(capturer.Width, capturer.Height, cfg.TargetFPS, cfg.BitrateKbps); err != nil {
		return nil, nil, err
	}
	return mf, nil, nil
}

func runCaptureEncodePipeline(
	ctx context.Context,
	cfg config.Config,
	log *logger.RollingLogger,
	rtcSrv *lan_rtc.RTCServer,
	idrReqCh chan struct{},
	benchRemaining *atomic.Int32,
	benchExit bool,
	cancelApp context.CancelFunc,
	readyCh chan<- error,
) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	capturer, err := capture.NewDXGICapturer()
	if err != nil {
		readyCh <- fmt.Errorf("DXGI init: %w", err)
		return
	}
	defer capturer.Close()

	enc, texEnc, err := initBestEncoder(capturer, cfg, log)
	if err != nil {
		readyCh <- fmt.Errorf("encoder init: %w", err)
		return
	}
	defer enc.Close()

	rtcSrv.SetScreenSize(capturer.Width, capturer.Height)
	log.Infof("DXGI + %s initialized (%dx%d @ %dfps, %dkbps, zero_copy=%v)",
		enc.Name(), capturer.Width, capturer.Height, cfg.TargetFPS, cfg.BitrateKbps, texEnc != nil)
	readyCh <- nil

	frameInterval := time.Second / time.Duration(cfg.TargetFPS)
	timeoutMs := uint32(16)
	if cfg.TargetFPS > 0 {
		ms := 1000 / cfg.TargetFPS
		if ms < 8 {
			ms = 8
		}
		timeoutMs = uint32(ms)
	}

	var (
		prevW        = capturer.Width
		prevH        = capturer.Height
		lastSentAt   time.Time
		winStart     = time.Now()
		winFrames    int
		winBytes     int64
		winDur       time.Duration
		benchCapNs   int64
		benchCompNs  int64
		benchEncNs   int64
		benchCount   int64
		cursorFrames int64
	)

	for {
		if ctx.Err() != nil {
			return
		}

		benchLeft := benchRemaining.Load()
		activePeers := rtcSrv.ActivePeerCount()

		if activePeers == 0 && benchLeft <= 0 {
			time.Sleep(50 * time.Millisecond)
			continue
		}

		forceIDR := false
		select {
		case <-idrReqCh:
			forceIDR = true
		default:
		}

		forceKeepalive := forceIDR || benchLeft > 0 || time.Since(lastSentAt) >= 500*time.Millisecond
		capTimeout := timeoutMs
		if benchLeft > 0 {
			capTimeout = 0 // immediate poll during micro-benchmark
		}

		frameStart := time.Now()
		tex, w, h, cursorMoved, capErr := capturer.AcquireGPUFrame(capTimeout, forceKeepalive)
		capDur := time.Since(frameStart)

		if errors.Is(capErr, capture.ErrAccessLost) {
			log.Warnf("DXGI_ERROR_ACCESS_LOST detected (mode/UAC change), reinitializing pipeline...")
			enc.Close()
			if rErr := capturer.Reinit(); rErr != nil {
				log.Warnf("DXGI reinit retry pending: %v", rErr)
				time.Sleep(150 * time.Millisecond)
				continue
			}
			newEnc, newTexEnc, eErr := initBestEncoder(capturer, cfg, log)
			if eErr != nil {
				log.Errorf("Encoder reinit failed: %v", eErr)
				time.Sleep(150 * time.Millisecond)
				continue
			}
			enc = newEnc
			texEnc = newTexEnc
			if capturer.Width != prevW || capturer.Height != prevH {
				prevW, prevH = capturer.Width, capturer.Height
				rtcSrv.SetScreenSize(capturer.Width, capturer.Height)
				log.Infof("DXGI+Encoder reinitialized at %dx%d", capturer.Width, capturer.Height)
			}
			continue
		}

		if errors.Is(capErr, capture.ErrTimeout) {
			continue
		}
		if capErr != nil {
			log.Warnf("Capture error: %v", capErr)
			time.Sleep(50 * time.Millisecond)
			continue
		}
		if cursorMoved {
			cursorFrames++
		}

		encStart := time.Now()
		var encoded encoder.EncodedFrame
		var encErr error
		if texEnc != nil {
			encoded, encErr = texEnc.EncodeTexture(tex, forceIDR || (benchLeft > 0 && benchCount == 0))
		} else {
			bgra, cw, _, rErr := capturer.ReadComposedToCPU()
			if rErr != nil {
				log.Warnf("ReadComposedToCPU error: %v", rErr)
				continue
			}
			encoded, encErr = enc.Encode(bgra, cw*4, forceIDR || (benchLeft > 0 && benchCount == 0))
		}
		if encErr != nil {
			log.Errorf("Encode error: %v", encErr)
			continue
		}
		encDur := time.Since(encStart)
		elapsed := time.Since(frameStart)

		if activePeers > 0 && len(encoded.Data) > 0 {
			if wErr := rtcSrv.WriteVideoSample(encoded.Data, frameInterval); wErr != nil {
				log.Warnf("WriteVideoSample error: %v", wErr)
			}
		}
		lastSentAt = time.Now()

		winFrames++
		winBytes += int64(len(encoded.Data))
		winDur += elapsed

		if benchLeft > 0 {
			benchCapNs += capDur.Nanoseconds()
			benchCompNs += capturer.LastComposeDuration().Nanoseconds()
			benchEncNs += encDur.Nanoseconds()
			benchCount++
			rem := benchRemaining.Add(-1)
			if rem == 0 {
				avgCapMs := float64(benchCapNs) / float64(benchCount) / 1e6
				avgCompUs := float64(benchCompNs) / float64(benchCount) / 1e3
				avgEncMs := float64(benchEncNs) / float64(benchCount) / 1e6
				log.Infof("BENCHMARK_RESULT encoder=%q frames=%d resolution=%dx%d avg_capture_ms=%.2f avg_cursor_compose_us=%.1f avg_encode_ms=%.2f avg_total_ms=%.2f cursor_moved_frames=%d last_au_bytes=%d keyframe=%v",
					enc.Name(), benchCount, w, h, avgCapMs, avgCompUs, avgEncMs, avgCapMs+avgEncMs, cursorFrames, len(encoded.Data), encoded.IsKeyFrame)
				if benchExit {
					cancelApp()
					return
				}
			}
		}

		if dt := time.Since(winStart); dt >= time.Second {
			avgMs := float64(winDur.Microseconds()) / float64(winFrames) / 1000.0
			fps := float64(winFrames) / dt.Seconds()
			kbps := (float64(winBytes) * 8.0) / dt.Seconds() / 1000.0
			rtcSrv.UpdateStats(lan_rtc.HostStats{
				CaptureWidth:  w,
				CaptureHeight: h,
				EncodedFPS:    fps,
				BitrateKbps:   kbps,
				EncodeAvgMs:   avgMs,
				EncoderName:   enc.Name(),
			})
			winStart = time.Now()
			winFrames = 0
			winBytes = 0
			winDur = 0
		}

		if sleepDur := frameInterval - elapsed; sleepDur > time.Millisecond && benchLeft <= 0 {
			time.Sleep(sleepDur)
		}
	}
}
