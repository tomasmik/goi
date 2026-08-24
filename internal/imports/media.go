package imports

import (
	"archive/zip"
	"context"
	"encoding/json"
	"fmt"
	"html"
	"strings"

	"github.com/tomasmik/goi/internal/media"
)

type mediaResolver struct {
	archive  *zip.ReadCloser
	manifest map[string]string
	files    map[string]*zip.File
}

func newMediaResolver(ctx context.Context, path string) (*mediaResolver, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	archive, err := zip.OpenReader(path)
	if err != nil {
		return nil, fmt.Errorf("open Anki media archive: %w", err)
	}
	resolver := &mediaResolver{archive: archive, manifest: make(map[string]string), files: make(map[string]*zip.File)}
	for _, entry := range archive.File {
		if err := ctx.Err(); err != nil {
			resolver.Close()
			return nil, err
		}
		if _, exists := resolver.files[entry.Name]; exists {
			resolver.Close()
			return nil, fmt.Errorf("Anki archive contains duplicate entry %q", entry.Name)
		}
		resolver.files[entry.Name] = entry
		if entry.Name != "media" {
			continue
		}
		manifest, err := readZipEntryContext(ctx, entry, 16<<20)
		if err != nil {
			resolver.Close()
			return nil, fmt.Errorf("read Anki media manifest: %w", err)
		}
		var archiveNames map[string]string
		if err := json.Unmarshal(manifest, &archiveNames); err != nil {
			resolver.Close()
			return nil, fmt.Errorf("parse Anki media manifest: %w", err)
		}
		for archiveName, originalName := range archiveNames {
			if previous, exists := resolver.manifest[originalName]; exists && previous != archiveName {
				resolver.Close()
				return nil, fmt.Errorf("Anki media manifest maps %q more than once", originalName)
			}
			resolver.manifest[originalName] = archiveName
		}
	}
	return resolver, nil
}

func (r *mediaResolver) Close() error {
	if r == nil || r.archive == nil {
		return nil
	}
	return r.archive.Close()
}

func (r *mediaResolver) Resolve(ctx context.Context, note Note, mapping Mapping) (*media.Upload, *media.Upload, error) {
	var audio, picture *media.Upload
	if mapping.AudioField >= 0 {
		var err error
		audio, err = r.resolveField(ctx, field(note, mapping.AudioField), media.KindAudio)
		if err != nil {
			return nil, nil, err
		}
	}
	if mapping.PictureField >= 0 {
		var err error
		picture, err = r.resolveField(ctx, field(note, mapping.PictureField), media.KindImage)
		if err != nil {
			return nil, nil, err
		}
	}
	return audio, picture, nil
}

func (r *mediaResolver) resolveField(ctx context.Context, value string, kind media.Kind) (*media.Upload, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	name := mediaReference(value, kind)
	if name == "" {
		return nil, nil
	}
	entryName := name
	if mapped, ok := r.manifest[name]; ok {
		entryName = mapped
	}
	entry, ok := r.files[entryName]
	if !ok {
		return nil, fmt.Errorf("referenced Anki media %q is missing", name)
	}
	content, err := readZipEntryContext(ctx, entry, media.MaxUploadBytes)
	if err != nil {
		return nil, fmt.Errorf("read referenced Anki media %q: %w", name, err)
	}
	upload, err := media.Prepare(kind, name, content)
	if err != nil {
		return nil, fmt.Errorf("validate referenced Anki media %q: %w", name, err)
	}
	return &upload, nil
}

func mediaReference(value string, kind media.Kind) string {
	if kind == media.KindAudio {
		start := strings.Index(value, "[sound:")
		if start < 0 {
			return ""
		}
		start += len("[sound:")
		end := strings.IndexByte(value[start:], ']')
		if end < 0 {
			return ""
		}
		return cleanMediaReference(value[start : start+end])
	}
	for _, marker := range []string{"src=\"", "src='"} {
		start := strings.Index(value, marker)
		if start < 0 {
			continue
		}
		start += len(marker)
		end := strings.IndexAny(value[start:], "\"'")
		if end >= 0 {
			return cleanMediaReference(value[start : start+end])
		}
	}
	return ""
}

func cleanMediaReference(value string) string {
	return strings.TrimSpace(html.UnescapeString(value))
}
