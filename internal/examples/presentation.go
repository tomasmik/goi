package examples

import (
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

func LinkForSource(sourceURL string, positionMS *int64) string {
	parsed, err := url.Parse(strings.TrimSpace(sourceURL))
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return ""
	}
	parsed.User = nil
	if positionMS == nil || *positionMS < 0 || !isYouTubeHost(parsed.Hostname()) {
		return parsed.String()
	}
	query := parsed.Query()
	query.Set("t", strconv.FormatInt(*positionMS/1000, 10)+"s")
	parsed.RawQuery = query.Encode()
	return parsed.String()
}

func FormatSourcePosition(positionMS *int64) string {
	if positionMS == nil || *positionMS < 0 {
		return ""
	}
	totalSeconds := *positionMS / 1000
	hours := totalSeconds / 3600
	minutes := totalSeconds % 3600 / 60
	seconds := totalSeconds % 60
	if hours > 0 {
		return fmt.Sprintf("%d:%02d:%02d", hours, minutes, seconds)
	}
	return fmt.Sprintf("%d:%02d", minutes, seconds)
}

func SplitTarget(sentence, target string) (before, matched, after string, found bool) {
	if target == "" {
		return sentence, "", "", false
	}
	index := strings.Index(sentence, target)
	if index < 0 {
		return sentence, "", "", false
	}
	return sentence[:index], sentence[index : index+len(target)], sentence[index+len(target):], true
}

func isYouTubeHost(host string) bool {
	host = strings.ToLower(host)
	return host == "youtu.be" || host == "youtube.com" || strings.HasSuffix(host, ".youtube.com") ||
		host == "youtube-nocookie.com" || strings.HasSuffix(host, ".youtube-nocookie.com")
}
