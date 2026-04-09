package main

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
)

type spillManager struct {
	service   string
	dir       string
	maxBytes  int64
	rotations int

	mu       sync.Mutex
	file     *os.File
	fileSize int64
}

func newSpillManager(service string, dir string, maxBytes int64, rotations int) *spillManager {
	return &spillManager{
		service:   spillSafeName(service),
		dir:       dir,
		maxBytes:  maxBytes,
		rotations: rotations,
	}
}

func (s *spillManager) WriteLine(message string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := os.MkdirAll(s.dir, 0755); err != nil {
		return err
	}
	line := message + "\n"
	if err := s.ensureCurrentLocked(int64(len(line))); err != nil {
		return err
	}
	n, err := s.file.WriteString(line)
	s.fileSize += int64(n)
	if err != nil {
		return err
	}
	return s.file.Sync()
}

func (s *spillManager) HasSpill() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.file != nil && s.fileSize > 0 {
		return true
	}
	for i := 0; i <= s.rotations; i++ {
		info, err := os.Stat(s.pathForIndex(i))
		if err == nil && info.Size() > 0 {
			return true
		}
	}
	return false
}

func (s *spillManager) ReplayTo(writeLine func(string) error) error {
	paths, err := s.prepareReplayPaths()
	if err != nil {
		return err
	}
	for _, path := range paths {
		if err := replayFile(path, writeLine); err != nil {
			return err
		}
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}

func (s *spillManager) prepareReplayPaths() ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.file != nil {
		if err := s.file.Close(); err != nil {
			return nil, err
		}
		s.file = nil
		s.fileSize = 0
	}

	paths := make([]string, 0, s.rotations+1)
	for i := s.rotations; i >= 1; i-- {
		path := s.pathForIndex(i)
		info, err := os.Stat(path)
		if err == nil && info.Size() > 0 {
			paths = append(paths, path)
		}
	}
	current := s.pathForIndex(0)
	if info, err := os.Stat(current); err == nil && info.Size() > 0 {
		paths = append(paths, current)
	}
	return paths, nil
}

func (s *spillManager) ensureCurrentLocked(nextWrite int64) error {
	if s.file == nil {
		file, err := os.OpenFile(s.pathForIndex(0), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
		if err != nil {
			return err
		}
		info, err := file.Stat()
		if err != nil {
			_ = file.Close()
			return err
		}
		s.file = file
		s.fileSize = info.Size()
	}
	if s.fileSize+nextWrite <= s.maxBytes {
		return nil
	}
	if err := s.rotateLocked(); err != nil {
		return err
	}
	file, err := os.OpenFile(s.pathForIndex(0), os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	s.file = file
	s.fileSize = 0
	return nil
}

func (s *spillManager) rotateLocked() error {
	if s.file != nil {
		if err := s.file.Close(); err != nil {
			return err
		}
		s.file = nil
		s.fileSize = 0
	}
	oldest := s.pathForIndex(s.rotations)
	if err := os.Remove(oldest); err != nil && !os.IsNotExist(err) {
		return err
	}
	for i := s.rotations - 1; i >= 0; i-- {
		from := s.pathForIndex(i)
		to := s.pathForIndex(i + 1)
		if err := os.Rename(from, to); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("rotate %s -> %s: %w", from, to, err)
		}
	}
	return nil
}

func (s *spillManager) pathForIndex(index int) string {
	if index <= 0 {
		return filepath.Join(s.dir, s.service+".log")
	}
	return filepath.Join(s.dir, s.service+".log."+strconv.Itoa(index))
}

func replayFile(path string, writeLine func(string) error) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	buffer := make([]byte, 0, 64*1024)
	scanner.Buffer(buffer, 1024*1024)
	for scanner.Scan() {
		message := strings.TrimRight(scanner.Text(), "\r\n")
		if message == "" {
			continue
		}
		if err := writeLine(message); err != nil {
			return err
		}
	}
	return scanner.Err()
}

func spillSafeName(service string) string {
	service = strings.TrimSpace(service)
	if service == "" {
		return "service"
	}
	replacer := strings.NewReplacer("/", "_", "\\", "_", " ", "_")
	return replacer.Replace(service)
}
