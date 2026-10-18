// Package mimetypes registers a consistent extension table so MIME types do
// not depend on the host's /etc/mime.types (absent in minimal containers).
package mimetypes

import (
	"mime"
	"path"
	"strings"
)

var table = map[string]string{
	".mp4": "video/mp4", ".m4v": "video/mp4", ".mkv": "video/x-matroska", ".webm": "video/webm",
	".mov": "video/quicktime", ".avi": "video/x-msvideo", ".wmv": "video/x-ms-wmv", ".flv": "video/x-flv",
	".ts": "video/mp2t", ".mpg": "video/mpeg", ".mpeg": "video/mpeg", ".3gp": "video/3gpp",
	".mp3": "audio/mpeg", ".wav": "audio/wav", ".flac": "audio/flac", ".ogg": "audio/ogg", ".opus": "audio/ogg",
	".m4a": "audio/mp4", ".aac": "audio/aac", ".wma": "audio/x-ms-wma",
	".jpg": "image/jpeg", ".jpeg": "image/jpeg", ".png": "image/png", ".gif": "image/gif", ".webp": "image/webp",
	".bmp": "image/bmp", ".tif": "image/tiff", ".tiff": "image/tiff", ".heic": "image/heic", ".heif": "image/heif",
	".avif": "image/avif", ".svg": "image/svg+xml", ".ico": "image/x-icon", ".jxr": "image/jxr",
	".pdf": "application/pdf", ".txt": "text/plain", ".md": "text/markdown", ".csv": "text/csv", ".log": "text/plain",
	".ini": "text/plain", ".cfg": "text/plain", ".yaml": "text/yaml", ".yml": "text/yaml", ".toml": "text/plain",
	".json": "application/json", ".xml": "application/xml", ".html": "text/html", ".htm": "text/html",
	".css": "text/css", ".js": "text/javascript", ".rtf": "application/rtf",
	".doc": "application/msword", ".docx": "application/vnd.openxmlformats-officedocument.wordprocessingml.document",
	".xls": "application/vnd.ms-excel", ".xlsx": "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
	".ppt": "application/vnd.ms-powerpoint", ".pptx": "application/vnd.openxmlformats-officedocument.presentationml.presentation",
	".odt": "application/vnd.oasis.opendocument.text", ".ods": "application/vnd.oasis.opendocument.spreadsheet",
	".zip": "application/zip", ".7z": "application/x-7z-compressed", ".rar": "application/vnd.rar",
	".gz": "application/gzip", ".tgz": "application/gzip", ".tar": "application/x-tar", ".bz2": "application/x-bzip2", ".xz": "application/x-xz",
	".iso": "application/x-iso9660-image", ".exe": "application/vnd.microsoft.portable-executable", ".msi": "application/x-msi",
	".apk": "application/vnd.android.package-archive", ".bin": "application/octet-stream",
	".ttf": "font/ttf", ".otf": "font/otf", ".woff": "font/woff", ".woff2": "font/woff2",
	".srt": "text/plain", ".vtt": "text/vtt", ".sql": "text/plain", ".go": "text/plain", ".py": "text/plain",
	".ps1": "text/plain", ".sh": "text/plain", ".bat": "text/plain", ".c": "text/plain", ".cpp": "text/plain",
	".h": "text/plain", ".java": "text/plain", ".ts.txt": "text/plain", ".tsx": "text/plain", ".jsx": "text/plain",
}

func init() {
	for ext, t := range table {
		_ = mime.AddExtensionType(ext, t)
	}
}

// ByName returns the MIME type for a file name ("application/octet-stream"
// when unknown). Parameters such as charset are stripped.
func ByName(name string) string {
	ext := strings.ToLower(path.Ext(name))
	if t, ok := table[ext]; ok {
		return t
	}
	t := mime.TypeByExtension(ext)
	if i := strings.IndexByte(t, ';'); i >= 0 {
		t = t[:i]
	}
	if t == "" {
		return "application/octet-stream"
	}
	return t
}
