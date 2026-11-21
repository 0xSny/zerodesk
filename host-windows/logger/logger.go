package logger

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"
)

type RollingLogger struct {
	mu       sync.Mutex
	dir      string
	fileName string
	maxBytes int64
	maxFiles int
	file     *os.File
	curSize  int64
	console  io.Writer
}

func NewRollingLogger(logDir, fileName string, maxBytes int64, maxFiles int) (*RollingLogger, error) {
	if err := os.MkdirAll(logDir, 0755); err != nil {
		return nil, err
	}
	if maxBytes <= 0 {
		maxBytes = 5 * 1024 * 1024
	}
	if maxFiles <= 0 {
		maxFiles = 5
	}
	l := &RollingLogger{
		dir:      logDir,
		fileName: fileName,
		maxBytes: maxBytes,
		maxFiles: maxFiles,
		console:  os.Stdout,
	}
	if err := l.openCurrent(); err != nil {
		return nil, err
	}
	return l, nil
}

func (l *RollingLogger) openCurrent() error {
	fullPath := filepath.Join(l.dir, l.fileName)
	f, err := os.OpenFile(fullPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return err
	}
	info, err := f.Stat()
	if err == nil {
		l.curSize = info.Size()
	}
	l.file = f
	return nil
}

func (l *RollingLogger) rotate() error {
	if l.file != nil {
		_ = l.file.Close()
		l.file = nil
	}
	base := filepath.Join(l.dir, l.fileName)
	for i := l.maxFiles - 1; i >= 1; i-- {
		src := fmt.Sprintf("%s.%d", base, i)
		dst := fmt.Sprintf("%s.%d", base, i+1)
		if i == l.maxFiles-1 {
			_ = os.Remove(dst)
		}
		if _, err := os.Stat(src); err == nil {
			_ = os.Rename(src, dst)
		}
	}
	if _, err := os.Stat(base); err == nil {
		_ = os.Rename(base, fmt.Sprintf("%s.1", base))
	}
	l.curSize = 0
	return l.openCurrent()
}

func (l *RollingLogger) Logf(level, format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	line := fmt.Sprintf("%s [%s] %s\n", time.Now().Format("2006-01-02 15:04:05.000"), level, msg)

	l.mu.Lock()
	defer l.mu.Unlock()

	if l.console != nil {
		_, _ = io.WriteString(l.console, line)
	}
	if l.file != nil {
		if l.curSize+int64(len(line)) > l.maxBytes {
			_ = l.rotate()
		}
		if l.file != nil {
			n, _ := l.file.WriteString(line)
			l.curSize += int64(n)
		}
	}
}

func (l *RollingLogger) Infof(format string, args ...any)  { l.Logf("INFO", format, args...) }
func (l *RollingLogger) Warnf(format string, args ...any)  { l.Logf("WARN", format, args...) }
func (l *RollingLogger) Errorf(format string, args ...any) { l.Logf("ERROR", format, args...) }

func (l *RollingLogger) Close() {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.file != nil {
		_ = l.file.Close()
		l.file = nil
	}
}
