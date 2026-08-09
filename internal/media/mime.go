package media

import (
	"bytes"
	"path"
	"strings"
)

// mimeToExt 常见多媒体 MIME → 文件扩展名映射。
// 用于对象 key 命名（sha256[:16] + 扩展名）。
var mimeToExt = map[string]string{
	"image/jpeg":               ".jpg",
	"image/png":                ".png",
	"image/gif":                ".gif",
	"image/webp":               ".webp",
	"image/bmp":                ".bmp",
	"image/svg+xml":            ".svg",
	"image/avif":               ".avif",
	"audio/ogg":                ".ogg",
	"audio/opus":               ".opus",
	"audio/mpeg":               ".mp3",
	"audio/mp4":                ".m4a",
	"audio/x-m4a":              ".m4a",
	"audio/wav":                ".wav",
	"audio/x-wav":              ".wav",
	"audio/flac":               ".flac",
	"audio/x-ms-wma":           ".wma",
	"audio/aac":                ".aac",
	"video/mp4":                ".mp4",
	"video/webm":               ".webm",
	"video/quicktime":          ".mov",
	"video/x-matroska":         ".mkv",
	"application/pdf":          ".pdf",
	"application/zip":          ".zip",
	"application/json":         ".json",
	"application/octet-stream": ".bin",
}

// extForMime 返回 MIME 对应的扩展名；未知类型回退 .bin。
func extForMime(mime string) string {
	key := strings.ToLower(strings.TrimSpace(mime))
	if ext, ok := mimeToExt[key]; ok {
		return ext
	}
	// 处理带参数的 MIME，如 "image/jpeg; charset=utf-8"
	if idx := strings.IndexByte(key, ';'); idx > 0 {
		if ext, ok := mimeToExt[strings.TrimSpace(key[:idx])]; ok {
			return ext
		}
	}
	return ".bin"
}

// sniffMime 基于文件头（magic bytes）嗅探 MIME 类型。
// 上游下载的媒体文件经常缺失或不带正确的 Content-Type，嗅探用于兜底。
// 无法识别时返回 application/octet-stream。
func sniffMime(data []byte) string {
	if len(data) < 12 {
		return "application/octet-stream"
	}
	switch {
	case bytes.HasPrefix(data, []byte("\x89PNG\r\n\x1a\n")):
		return "image/png"
	case bytes.HasPrefix(data, []byte("\xff\xd8\xff")):
		return "image/jpeg"
	case bytes.HasPrefix(data, []byte("GIF87a")) || bytes.HasPrefix(data, []byte("GIF89a")):
		return "image/gif"
	case bytes.HasPrefix(data, []byte("RIFF")) && len(data) >= 12 && string(data[8:12]) == "WEBP":
		return "image/webp"
	case bytes.HasPrefix(data, []byte("BM")) && len(data) > 14:
		return "image/bmp"
	case bytes.HasPrefix(data, []byte("OggS")):
		return "audio/ogg"
	case bytes.HasPrefix(data, []byte("fLaC")):
		return "audio/flac"
	case bytes.HasPrefix(data, []byte("ID3")) || (bytes.HasPrefix(data, []byte("\xff\xfb")) || bytes.HasPrefix(data, []byte("\xff\xf3"))):
		return "audio/mpeg"
	case bytes.HasPrefix(data, []byte("%PDF")):
		return "application/pdf"
	case bytes.HasPrefix(data, []byte("PK\x03\x04")):
		return "application/zip"
	case bytes.HasPrefix(data, []byte("\x1aE\xdf\xa3")):
		return "video/webm" // Matroska/WebM 容器
	case len(data) >= 12 && string(data[4:8]) == "ftyp":
		// ISO BMFF（mp4 / m4a / mov 等）
		switch string(data[8:12]) {
		case "isom", "mp42", "avc1", "M4V ":
			return "video/mp4"
		case "M4A ", "mp4a":
			return "audio/mp4"
		default:
			return "video/mp4"
		}
	}
	return "application/octet-stream"
}

// extFromPath 从路径/URL 中提取扩展名（小写，含点）；无扩展名返回 ""。
func extFromPath(p string) string {
	ext := strings.ToLower(path.Ext(strings.SplitN(p, "?", 2)[0]))
	if len(ext) > 0 && len(ext) <= 8 {
		return ext
	}
	return ""
}

// SniffMime 导出文件头嗅探能力（供 bot 层下载兜底使用）。
func SniffMime(data []byte) string {
	if len(data) == 0 {
		return "application/octet-stream"
	}
	return sniffMime(data)
}
