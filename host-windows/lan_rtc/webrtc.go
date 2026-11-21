package lan_rtc

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/pion/ice/v4"
	"github.com/pion/rtcp"
	"github.com/pion/webrtc/v4"
	"github.com/pion/webrtc/v4/pkg/media"

	"zerodesk-host/config"
	"zerodesk-host/input"
	"zerodesk-host/logger"
	"zerodesk-host/system_ctrl"
)

type ConnectRequest struct {
	Pin  string `json:"pin"`
	SDP  string `json:"sdp"`
	Type string `json:"type"`
}

type ConnectResponse struct {
	SDP         string `json:"sdp"`
	Type        string `json:"type"`
	Width       int    `json:"width"`
	Height      int    `json:"height"`
	TargetFPS   int    `json:"target_fps"`
	BitrateKbps int    `json:"bitrate_kbps"`
}

type ControlMessage struct {
	Type    string  `json:"type"`
	DX      float64 `json:"dx,omitempty"`
	DY      float64 `json:"dy,omitempty"`
	Button  string  `json:"button,omitempty"`
	Action  string  `json:"action,omitempty"`
	Text    string  `json:"text,omitempty"`
	Key     string  `json:"key,omitempty"`
	Seconds int     `json:"seconds,omitempty"`
	TS      int64   `json:"ts,omitempty"`
}

type HostStats struct {
	CaptureWidth    int     `json:"width"`
	CaptureHeight   int     `json:"height"`
	EncodedFPS      float64 `json:"encoded_fps"`
	BitrateKbps     float64 `json:"bitrate_kbps"`
	EncodeAvgMs     float64 `json:"encode_avg_ms"`
	ActivePeers     int32   `json:"active_peers"`
	EncoderName     string  `json:"encoder_name"`
	BlackoutEnabled bool    `json:"blackout_enabled"`
}

type RTCServer struct {
	cfg        config.Config
	log        *logger.RollingLogger
	api        *webrtc.API
	videoTrack *webrtc.TrackLocalStaticSample
	blackout   *system_ctrl.BlackoutController

	mu          sync.Mutex
	peers       map[*webrtc.PeerConnection]*webrtc.DataChannel
	peerCount   atomic.Int32
	idrReqCh    chan struct{}
	screenWidth atomic.Int32
	screenH     atomic.Int32
	statsMu     sync.RWMutex
	stats       HostStats
}

func NewRTCServer(cfg config.Config, log *logger.RollingLogger, idrReqCh chan struct{}, blackout *system_ctrl.BlackoutController) (*RTCServer, error) {
	me := &webrtc.MediaEngine{}
	h264Feedback := []webrtc.RTCPFeedback{
		{Type: "nack"},
		{Type: "nack", Parameter: "pli"},
		{Type: "ccm", Parameter: "fir"},
	}

	// Register multiple H.264 profiles: Apple VideoToolbox Constrained High (640c32/640c1f) first,
	// then High (640032/64001f) for sharp 2560x1440 text, plus Constrained Baseline (42e01f/42001f) fallback.
	h264Profiles := []struct {
		fmtp string
		pt   webrtc.PayloadType
	}{
		{"level-asymmetry-allowed=1;packetization-mode=1;profile-level-id=640c32", 96},
		{"level-asymmetry-allowed=1;packetization-mode=1;profile-level-id=640c1f", 98},
		{"level-asymmetry-allowed=1;packetization-mode=1;profile-level-id=640032", 100},
		{"level-asymmetry-allowed=1;packetization-mode=1;profile-level-id=64001f", 102},
		{"level-asymmetry-allowed=1;packetization-mode=1;profile-level-id=42e01f", 104},
		{"level-asymmetry-allowed=1;packetization-mode=1;profile-level-id=42001f", 106},
	}
	for _, p := range h264Profiles {
		if err := me.RegisterCodec(webrtc.RTPCodecParameters{
			RTPCodecCapability: webrtc.RTPCodecCapability{
				MimeType:     webrtc.MimeTypeH264,
				ClockRate:    90000,
				SDPFmtpLine:  p.fmtp,
				RTCPFeedback: h264Feedback,
			},
			PayloadType: p.pt,
		}, webrtc.RTPCodecTypeVideo); err != nil {
			return nil, err
		}
	}

	// Build physical-NIC-first allowlist for ICE host candidates
	allowedIPs := make(map[string]struct{})
	for _, ipStr := range GetFilteredLANIPs(cfg.FilterVirtualNICs) {
		allowedIPs[ipStr] = struct{}{}
	}

	// Pure LAN WebRTC: disable mDNS and filter strictly to allowed LAN IPv4 host candidates
	se := webrtc.SettingEngine{}
	se.SetNetworkTypes([]webrtc.NetworkType{webrtc.NetworkTypeUDP4})
	se.SetICEMulticastDNSMode(ice.MulticastDNSModeDisabled)
	se.SetIPFilter(func(ip net.IP) bool {
		ip4 := ip.To4()
		if ip4 == nil {
			return false
		}
		if len(allowedIPs) > 0 {
			_, ok := allowedIPs[ip4.String()]
			return ok
		}
		return IsPrivateLANIPv4(ip4)
	})
	se.SetIncludeLoopbackCandidate(true) // allows local smoke-test verification on 127.0.0.1 while rejecting external/virtual IPs

	api := webrtc.NewAPI(
		webrtc.WithMediaEngine(me),
		webrtc.WithSettingEngine(se),
	)

	// Empty SDPFmtpLine lets Pion match any negotiated H.264 profile (High 640032/64001f or Baseline 42e01f)
	videoTrack, err := webrtc.NewTrackLocalStaticSample(
		webrtc.RTPCodecCapability{
			MimeType:  webrtc.MimeTypeH264,
			ClockRate: 90000,
		},
		"zerodesk-video",
		"zerodesk-stream",
	)
	if err != nil {
		return nil, fmt.Errorf("create H264 TrackLocalStaticSample failed: %w", err)
	}

	return &RTCServer{
		cfg:        cfg,
		log:        log,
		api:        api,
		videoTrack: videoTrack,
		blackout:   blackout,
		peers:      make(map[*webrtc.PeerConnection]*webrtc.DataChannel),
		idrReqCh:   idrReqCh,
	}, nil
}

func (s *RTCServer) SetScreenSize(w, h int) {
	s.screenWidth.Store(int32(w))
	s.screenH.Store(int32(h))
}

func (s *RTCServer) UpdateStats(st HostStats) {
	s.statsMu.Lock()
	s.stats = st
	s.statsMu.Unlock()
}

func (s *RTCServer) GetStats() HostStats {
	s.statsMu.RLock()
	st := s.stats
	s.statsMu.RUnlock()
	st.ActivePeers = s.peerCount.Load()
	if s.blackout != nil {
		st.BlackoutEnabled = s.blackout.IsEnabled()
	}
	return st
}

func (s *RTCServer) ActivePeerCount() int32 {
	return s.peerCount.Load()
}

// BroadcastBlackoutState notifies all connected DataChannels when blackout state changes (including Ctrl+Alt+B).
func (s *RTCServer) BroadcastBlackoutState(enabled bool) {
	payload, _ := json.Marshal(map[string]any{"type": "blackout_state", "enabled": enabled})
	text := string(payload)

	s.mu.Lock()
	dcs := make([]*webrtc.DataChannel, 0, len(s.peers))
	for _, dc := range s.peers {
		if dc != nil && dc.ReadyState() == webrtc.DataChannelStateOpen {
			dcs = append(dcs, dc)
		}
	}
	s.mu.Unlock()

	for _, dc := range dcs {
		_ = dc.SendText(text)
	}
}

// WriteVideoSample writes an H.264 Annex-B sample with explicit timestamp & frame duration into the WebRTC track.
func (s *RTCServer) WriteVideoSample(annexB []byte, duration time.Duration) error {
	if len(annexB) == 0 || s.peerCount.Load() == 0 {
		return nil
	}
	return s.videoTrack.WriteSample(media.Sample{
		Data:      annexB,
		Duration:  duration,
		Timestamp: time.Now(),
	})
}

func (s *RTCServer) requestIDR() {
	if s.idrReqCh == nil {
		return
	}
	select {
	case s.idrReqCh <- struct{}{}:
	default:
	}
}

// HandleConnect handles single-roundtrip POST /api/connect SDP Offer -> full ICE-gathered Answer.
func (s *RTCServer) HandleConnect(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req ConnectRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		http.Error(w, "invalid json payload", http.StatusBadRequest)
		return
	}

	if subtle.ConstantTimeCompare([]byte(req.Pin), []byte(s.cfg.PinCode)) != 1 {
		s.log.Warnf("Rejected /api/connect from %s: invalid PIN", r.RemoteAddr)
		http.Error(w, `{"error":"invalid pin"}`, http.StatusUnauthorized)
		return
	}

	if req.SDP == "" {
		http.Error(w, `{"error":"missing sdp"}`, http.StatusBadRequest)
		return
	}

	// Pure LAN configuration: ICEServers MUST remain empty (zero STUN/TURN, zero external traffic)
	pc, err := s.api.NewPeerConnection(webrtc.Configuration{
		ICEServers: []webrtc.ICEServer{},
	})
	if err != nil {
		s.log.Errorf("NewPeerConnection failed: %v", err)
		http.Error(w, "webrtc peer creation failed", http.StatusInternalServerError)
		return
	}

	sender, err := pc.AddTrack(s.videoTrack)
	if err != nil {
		_ = pc.Close()
		http.Error(w, "add video track failed", http.StatusInternalServerError)
		return
	}

	// Read RTCP feedback (PLI/FIR) to trigger immediate IDR keyframes on packet loss
	go func() {
		rtcpBuf := make([]byte, 1500)
		for {
			n, _, readErr := sender.Read(rtcpBuf)
			if readErr != nil {
				return
			}
			pkts, err := rtcp.Unmarshal(rtcpBuf[:n])
			if err != nil {
				continue
			}
			for _, pkt := range pkts {
				switch pkt.(type) {
				case *rtcp.PictureLossIndication, *rtcp.FullIntraRequest:
					s.requestIDR()
				}
			}
		}
	}()

	pc.OnDataChannel(func(dc *webrtc.DataChannel) {
		if dc.Label() != "control" {
			return
		}
		s.log.Infof("DataChannel 'control' opened from %s", r.RemoteAddr)
		dc.OnOpen(func() {
			s.mu.Lock()
			if _, exists := s.peers[pc]; exists {
				s.peers[pc] = dc
			}
			s.mu.Unlock()
			if s.blackout != nil {
				reply, _ := json.Marshal(map[string]any{"type": "blackout_state", "enabled": s.blackout.IsEnabled()})
				_ = dc.SendText(string(reply))
			}
		})
		dc.OnMessage(func(msg webrtc.DataChannelMessage) {
			s.handleControlMessage(dc, msg.Data)
		})
	})

	s.mu.Lock()
	s.peers[pc] = nil
	s.mu.Unlock()
	s.peerCount.Add(1)

	var removed atomic.Bool
	cleanupPeer := func() {
		if removed.Swap(true) {
			return
		}
		s.mu.Lock()
		delete(s.peers, pc)
		s.mu.Unlock()
		remaining := s.peerCount.Add(-1)
		_ = pc.Close()

		// Safety Insurance #1: automatically disable blackout curtain when no clients remain connected
		if remaining <= 0 && s.blackout != nil && s.blackout.IsEnabled() {
			s.log.Infof("All peers disconnected: auto-disabling blackout curtain for safety")
			_ = s.blackout.SetEnabled(false)
		}
	}

	pc.OnConnectionStateChange(func(state webrtc.PeerConnectionState) {
		s.log.Infof("Peer %s state -> %s", r.RemoteAddr, state.String())
		switch state {
		case webrtc.PeerConnectionStateConnected:
			s.requestIDR()
		case webrtc.PeerConnectionStateFailed, webrtc.PeerConnectionStateClosed, webrtc.PeerConnectionStateDisconnected:
			if state != webrtc.PeerConnectionStateDisconnected {
				cleanupPeer()
			} else {
				time.AfterFunc(3*time.Second, func() {
					if pc.ConnectionState() == webrtc.PeerConnectionStateDisconnected {
						cleanupPeer()
					}
				})
			}
		}
	})

	sdpType := webrtc.SDPTypeOffer
	if req.Type == "pranswer" {
		sdpType = webrtc.SDPTypePranswer
	}
	if err := pc.SetRemoteDescription(webrtc.SessionDescription{Type: sdpType, SDP: req.SDP}); err != nil {
		cleanupPeer()
		http.Error(w, fmt.Sprintf("SetRemoteDescription error: %v", err), http.StatusBadRequest)
		return
	}

	answer, err := pc.CreateAnswer(nil)
	if err != nil {
		cleanupPeer()
		http.Error(w, fmt.Sprintf("CreateAnswer error: %v", err), http.StatusInternalServerError)
		return
	}

	gatherDone := webrtc.GatheringCompletePromise(pc)
	if err := pc.SetLocalDescription(answer); err != nil {
		cleanupPeer()
		http.Error(w, fmt.Sprintf("SetLocalDescription error: %v", err), http.StatusInternalServerError)
		return
	}

	select {
	case <-gatherDone:
	case <-time.After(1500 * time.Millisecond):
		s.log.Warnf("ICE gathering timeout fallback triggered for %s", r.RemoteAddr)
	}

	localDesc := pc.LocalDescription()
	resp := ConnectResponse{
		SDP:         localDesc.SDP,
		Type:        localDesc.Type.String(),
		Width:       int(s.screenWidth.Load()),
		Height:      int(s.screenH.Load()),
		TargetFPS:   s.cfg.TargetFPS,
		BitrateKbps: s.cfg.BitrateKbps,
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
	s.requestIDR()
	s.log.Infof("Single-shot WebRTC handshake completed for %s", r.RemoteAddr)
}

func (s *RTCServer) handleControlMessage(dc *webrtc.DataChannel, raw []byte) {
	var msg ControlMessage
	if err := json.Unmarshal(raw, &msg); err != nil {
		return
	}
	switch msg.Type {
	case "ping":
		reply, _ := json.Marshal(map[string]any{"type": "pong", "ts": msg.TS})
		_ = dc.SendText(string(reply))
	case "mouse_move":
		_ = input.MoveMouseRelative(int32(msg.DX), int32(msg.DY))
	case "mouse_btn":
		_ = input.MouseButton(msg.Button, msg.Action)
	case "scroll":
		_ = input.Scroll(int32(msg.DX), int32(msg.DY))
	case "text":
		_ = input.InjectUnicodeText(msg.Text)
	case "shortcut":
		_ = input.SendShortcut(msg.Key)
	case "vol_mute":
		_ = system_ctrl.VolumeMute()
	case "vol_up":
		_ = system_ctrl.VolumeUp()
	case "vol_down":
		_ = system_ctrl.VolumeDown()
	case "blackout":
		if s.blackout != nil {
			switch msg.Action {
			case "on":
				_ = s.blackout.SetEnabled(true)
			case "off":
				_ = s.blackout.SetEnabled(false)
			case "toggle":
				s.blackout.Toggle()
			}
			reply, _ := json.Marshal(map[string]any{"type": "blackout_state", "enabled": s.blackout.IsEnabled()})
			_ = dc.SendText(string(reply))
		}
	case "sleep":
		s.log.Infof("Sleep command triggered via bedside control channel")
		if s.blackout != nil {
			_ = s.blackout.SetEnabled(false)
		}
		go func() {
			time.Sleep(150 * time.Millisecond)
			if err := system_ctrl.Sleep(); err != nil {
				s.log.Errorf("Sleep failed: %v", err)
			}
		}()
	case "shutdown":
		sec := msg.Seconds
		if sec <= 0 {
			sec = 60
		}
		if err := system_ctrl.ScheduleShutdown(sec); err != nil {
			s.log.Errorf("ScheduleShutdown failed: %v", err)
		} else {
			s.log.Infof("Scheduled shutdown in %ds", sec)
		}
	case "shutdown_cancel":
		if err := system_ctrl.CancelShutdown(); err != nil {
			s.log.Errorf("CancelShutdown failed: %v", err)
		} else {
			s.log.Infof("Cancelled scheduled shutdown")
		}
	case "request_idr":
		s.requestIDR()
	}
}

func (s *RTCServer) CloseAll(ctx context.Context) {
	s.mu.Lock()
	list := make([]*webrtc.PeerConnection, 0, len(s.peers))
	for pc := range s.peers {
		list = append(list, pc)
	}
	s.peers = make(map[*webrtc.PeerConnection]*webrtc.DataChannel)
	s.mu.Unlock()
	for _, pc := range list {
		_ = pc.Close()
	}
	if s.blackout != nil {
		_ = s.blackout.SetEnabled(false)
	}
}
