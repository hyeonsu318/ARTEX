package server

import (
	"archive/zip"
	"bytes"
	"compress/bzip2"
	"fmt"
	"io"
	"path"
	"strings"
	"unicode/utf8"

	"github.com/klauspost/compress/zstd"
	"golang.org/x/text/encoding/simplifiedchinese"
)

// Go 의 archive/zip 은 Store(0) 와 Deflate(8) 두 가지 압축 해제기만 내장하고 있어, 다른 방식을 만나면
// "zip: unsupported compression algorithm" 를 반환한다. 압축 프로그램은 기본이 아닌 설정에서 종종 다른 방식을 쓰므로
// (7-Zip 의 bzip2, WinZip 의 zstd) 순수 Go 로 풀 수 있는 두 가지를 여기서 보충한다. 정말 풀 수 없는
// (Deflate64 / LZMA / XZ / PPMd / 암호화 패키지) 경우에는 압축 해제 전에 한국어 안내를 내고, 하위
// 오류를 그대로 사용자에게 던지지 않는다.
const (
	zipMethodStore     = 0
	zipMethodDeflate   = 8
	zipMethodDeflate64 = 9
	zipMethodBzip2     = 12
	zipMethodLZMA      = 14
	zipMethodZstdPKW   = 20 // PKWARE 가 초기에 zstd 에 할당한 번호
	zipMethodZstd      = 93
	zipMethodXZ        = 95
	zipMethodJPEG      = 96
	zipMethodWavPack   = 97
	zipMethodPPMd      = 98
	zipMethodAES       = 99
)

var zipMethodNames = map[uint16]string{
	zipMethodStore:     "Store",
	zipMethodDeflate:   "Deflate",
	zipMethodDeflate64: "Deflate64",
	zipMethodBzip2:     "bzip2",
	zipMethodLZMA:      "LZMA",
	zipMethodZstdPKW:   "Zstandard",
	zipMethodZstd:      "Zstandard",
	zipMethodXZ:        "XZ",
	zipMethodJPEG:      "JPEG",
	zipMethodWavPack:   "WavPack",
	zipMethodPPMd:      "PPMd",
	zipMethodAES:       "AES 암호화",
}

func zipMethodName(m uint16) string {
	if n, ok := zipMethodNames[m]; ok {
		return n
	}
	return "알 수 없음"
}

// newSkillZipReader parses an uploaded archive and registers the extra decompressors
// we can support beyond the stdlib's Store/Deflate.
func newSkillZipReader(buf []byte) (*zip.Reader, error) {
	zr, err := zip.NewReader(bytes.NewReader(buf), int64(len(buf)))
	if err != nil {
		return nil, fmt.Errorf("압축 파일을 해석할 수 없습니다(zip 형식이어야 함): %w", err)
	}
	zr.RegisterDecompressor(zipMethodBzip2, func(r io.Reader) io.ReadCloser {
		return io.NopCloser(bzip2.NewReader(r))
	})
	zdec := zstd.ZipDecompressor(zstd.WithDecoderConcurrency(1))
	zr.RegisterDecompressor(zipMethodZstd, zdec)
	zr.RegisterDecompressor(zipMethodZstdPKW, zdec)
	return zr, nil
}

// skillZipEntry pairs a zip entry with its decoded (UTF-8) name — f.Name may hold
// raw GBK bytes, see zipEntryName.
type skillZipEntry struct {
	f    *zip.File
	name string
}

// skillZipEntries lists the archive's real files (no directory entries, no archiver
// junk) with their names decoded to UTF-8.
func skillZipEntries(zr *zip.Reader) []skillZipEntry {
	out := make([]skillZipEntry, 0, len(zr.File))
	for _, f := range zr.File {
		if f.FileInfo().IsDir() {
			continue
		}
		name := zipEntryName(f)
		if strings.HasPrefix(name, "__MACOSX/") || strings.Contains(name, "/__MACOSX/") ||
			path.Base(name) == ".DS_Store" {
			continue // macOS 패키징 잔여물
		}
		out = append(out, skillZipEntry{f: f, name: name})
	}
	return out
}

// zipEntryName returns the entry path as UTF-8. Windows 의 7-Zip / WinRAR / 탐색기는
// UTF-8 플래그를 설정하지 않으면 한국어/중국어 파일명을 GBK 로 zip 에 쓰고, Go 는 그 바이트를 그대로 보존하므로 이름이
// 유효한 UTF-8 도 아니고 경로 검증도 통과하지 못한다 — 여기서 GBK 로 대체 디코딩한다.
func zipEntryName(f *zip.File) string {
	if utf8.ValidString(f.Name) {
		return f.Name
	}
	if dec, err := simplifiedchinese.GBK.NewDecoder().String(f.Name); err == nil && utf8.ValidString(dec) {
		return dec
	}
	return f.Name
}

// checkSkillZipMethods rejects archives we cannot extract, naming the offending
// entry and method instead of letting f.Open() fail with an opaque English error.
func checkSkillZipMethods(entries []skillZipEntry) error {
	for _, e := range entries {
		if e.f.Flags&0x1 != 0 || e.f.Method == zipMethodAES {
			return fmt.Errorf("압축 파일이 암호화되어 있습니다(%s). 암호화되지 않은 zip 을 업로드하세요", e.name)
		}
		switch e.f.Method {
		case zipMethodStore, zipMethodDeflate, zipMethodBzip2, zipMethodZstd, zipMethodZstdPKW:
		default:
			return fmt.Errorf("지원되지 않는 압축 방식입니다 %s(method %d): %s. "+
				"「저장」 또는 「Deflate」 방식으로 다시 압축하세요 (7-Zip/WinRAR 은 압축 방식으로 Deflate 선택, "+
				"또는 시스템 기본 「압축 폴더로 보내기」 나 명령행 zip -r 을 사용하세요)",
				zipMethodName(e.f.Method), e.f.Method, e.name)
		}
	}
	return nil
}
