package web

const (
	phaseIdle            = "idle"
	phaseParseJSON       = "parse_json"
	phaseMetaCache       = "meta_cache"
	phaseResolvePeer     = "resolve_peer"
	phaseBuildList       = "build_list"
	phaseScanDisk        = "scan_disk"
	phaseFetchMessages   = "fetch_messages"
	phaseQueueImages     = "queue_images"
	phaseResumeDownloads = "resume_downloads"
)

const (
	sourceIdle     = "idle"
	sourceJSON     = "json"
	sourceCache    = "cache"
	sourceDisk     = "disk"
	sourceTelegram = "telegram"
	sourceDownload = "download"
)

func (s *Server) setImportPhase(phase, source, detail string) {
	s.mu.Lock()
	s.importPhase = phase
	s.importSource = source
	s.importDetail = detail
	s.mu.Unlock()
	s.notify()
}

func (s *Server) downloadActivityCounts() (downloading, queued int) {
	queuePos := s.queuePositions()
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, id := range s.order {
		it := s.items[id]
		if it == nil {
			continue
		}
		if it.Status == statusCaching {
			downloading++
			continue
		}
		if p, ok := queuePos[id]; ok && p > 0 {
			queued++
		}
	}
	return downloading, queued
}
