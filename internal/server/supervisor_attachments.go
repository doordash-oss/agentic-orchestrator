// Copyright 2026 DoorDash, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package server

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/doordash-oss/agentic-orchestrator/internal/errcat"
	"github.com/doordash-oss/agentic-orchestrator/internal/supervisor"
)

// supervisorAttachmentError is a typed 400 for an attachment that cannot
// be copied into the conversation (missing, not a regular file, or over
// its per-kind size cap).
type supervisorAttachmentError struct{ msg string }

func (e *supervisorAttachmentError) Error() string { return e.msg }

// supervisorAttachmentSource is one attached file before it is copied:
// a server-local path or a staged upload reference.
type supervisorAttachmentSource struct {
	descriptor supervisor.Attachment
	localPath  string
	ref        string
}

func supervisorAttachmentCap(kind string) int64 {
	if kind == supervisor.AttachmentKindImage {
		return maxUploadImageBytes
	}
	return maxUploadAttachmentBytes
}

func supervisorAttachmentCapError(name, kind string) error {
	if kind == supervisor.AttachmentKindImage {
		return &supervisorAttachmentError{msg: fmt.Sprintf("image %q exceeds the 10 MiB per-image limit", name)}
	}
	return &supervisorAttachmentError{msg: fmt.Sprintf("file %q exceeds the 25 MiB per-file limit", name)}
}

// supervisorAttachmentSources validates every attachment of a message
// without copying or consuming anything: combined counts, absolute regular
// local files within their per-kind cap (images also need an image
// extension), and well-formed staged references of the right kind. A
// consumed reference still describes itself here so an identical resend can
// deduplicate; the copy step later refuses it if the send is new. Sources
// come back in harness order: images, image uploads, files, file uploads.
func (h *apiHandler) supervisorAttachmentSources(req SupervisorMessageRequest) ([]supervisorAttachmentSource, error) {
	if len(req.Images)+len(req.ImageUploads) > maxFeatureImagesTotal {
		return nil, &supervisorAttachmentError{msg: fmt.Sprintf("too many images: images + image_uploads must be at most %d total", maxFeatureImagesTotal)}
	}
	if len(req.Attachments)+len(req.AttachmentUploads) > maxFeatureAttachmentsTotal {
		return nil, &supervisorAttachmentError{msg: fmt.Sprintf("too many attachments: attachments + attachment_uploads must be at most %d total", maxFeatureAttachmentsTotal)}
	}
	var sources []supervisorAttachmentSource
	local := func(path, kind string) error {
		if path == "" || strings.ContainsRune(path, 0) || !filepath.IsAbs(path) {
			return &supervisorAttachmentError{msg: fmt.Sprintf("attachment path %q must be an absolute server-local path", path)}
		}
		name := filepath.Base(path)
		if kind == supervisor.AttachmentKindImage && !allowedUploadImageExtensions[strings.ToLower(strings.TrimPrefix(filepath.Ext(name), "."))] {
			return &supervisorAttachmentError{msg: fmt.Sprintf("image %q needs a png, jpg, jpeg, gif, or webp file name extension", name)}
		}
		info, err := os.Stat(path)
		if err != nil || !info.Mode().IsRegular() {
			return &supervisorAttachmentError{msg: fmt.Sprintf("attachment path %q is not a readable regular file", path)}
		}
		if info.Size() > supervisorAttachmentCap(kind) {
			return supervisorAttachmentCapError(name, kind)
		}
		sources = append(sources, supervisorAttachmentSource{localPath: path, descriptor: supervisor.Attachment{Kind: kind, Name: name, Size: info.Size()}})
		return nil
	}
	seen := map[string]struct{}{}
	upload := func(ref, uploadKind, kind string) error {
		if _, dup := seen[ref]; dup {
			return &uploadReferenceError{msg: fmt.Sprintf("upload reference %q is listed more than once", ref)}
		}
		seen[ref] = struct{}{}
		meta, err := h.uploads.describe(ref, uploadKind)
		if err != nil {
			return err
		}
		sources = append(sources, supervisorAttachmentSource{ref: ref, descriptor: supervisor.Attachment{Kind: kind, Name: meta.Name, Size: meta.Size}})
		return nil
	}
	for _, path := range req.Images {
		if err := local(path, supervisor.AttachmentKindImage); err != nil {
			return nil, err
		}
	}
	for _, ref := range req.ImageUploads {
		if err := upload(ref, uploadKindImage, supervisor.AttachmentKindImage); err != nil {
			return nil, err
		}
	}
	for _, path := range req.Attachments {
		if err := local(path, supervisor.AttachmentKindFile); err != nil {
			return nil, err
		}
	}
	for _, ref := range req.AttachmentUploads {
		if err := upload(ref, uploadKindAttachment, supervisor.AttachmentKindFile); err != nil {
			return nil, err
		}
	}
	return sources, nil
}

// stageSupervisorAttachments returns the coordinator's Stage callback: it
// consumes the staged references and copies the local files into the
// conversation's attachments directory, every copy under a fresh opaque
// name that keeps the original extension. The returned finish commits the
// reference claims (deleting the staged sources) or removes every copy and
// releases the claims so the references stay valid for a retry.
func (h *apiHandler) stageSupervisorAttachments(sources []supervisorAttachmentSource) func(string) ([]supervisor.Attachment, func(bool), error) {
	return func(dir string) ([]supervisor.Attachment, func(bool), error) {
		var imageRefs, fileRefs []string
		for _, src := range sources {
			switch {
			case src.ref == "":
			case src.descriptor.Kind == supervisor.AttachmentKindImage:
				imageRefs = append(imageRefs, src.ref)
			default:
				fileRefs = append(fileRefs, src.ref)
			}
		}
		consumed, err := h.uploads.consumeNamed(imageRefs, fileRefs, dir, supervisorAttachmentName)
		if err != nil {
			return nil, nil, err
		}
		var localCopies []string
		rollback := func() {
			for _, path := range localCopies {
				_ = os.Remove(path)
			}
			consumed.rollback()
		}
		out := make([]supervisor.Attachment, 0, len(sources))
		var imageIdx, fileIdx int
		for _, src := range sources {
			a := src.descriptor
			switch {
			case src.ref != "" && a.Kind == supervisor.AttachmentKindImage:
				a.Path = consumed.imagePaths[imageIdx]
				imageIdx++
			case src.ref != "":
				a.Path = consumed.attachmentPaths[fileIdx]
				fileIdx++
			default:
				path, size, err := copySupervisorAttachment(src.localPath, dir, a.Name, a.Kind)
				if err != nil {
					rollback()
					return nil, nil, err
				}
				localCopies = append(localCopies, path)
				a.Path, a.Size = path, size
			}
			out = append(out, a)
		}
		return out, func(commit bool) {
			if commit {
				consumed.commit()
				return
			}
			rollback()
		}, nil
	}
}

// supervisorAttachmentName is the on-disk name of an attachment copy: 32
// random hex characters plus the original's safe extension.
func supervisorAttachmentName(ext string) (string, error) {
	id, err := newUploadReference()
	if err != nil {
		return "", err
	}
	return id + ext, nil
}

// copySupervisorAttachment copies one local file into dir, bounded by the
// per-kind cap even if the file grew after validation.
func copySupervisorAttachment(src, dir, name, kind string) (string, int64, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", 0, err
	}
	in, err := os.Open(src)
	if err != nil {
		return "", 0, &supervisorAttachmentError{msg: fmt.Sprintf("attachment path %q is not a readable regular file", src)}
	}
	defer in.Close()
	fileName, err := supervisorAttachmentName(safeUploadExtension(name))
	if err != nil {
		return "", 0, err
	}
	dest := filepath.Join(dir, fileName)
	out, err := os.OpenFile(dest, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return "", 0, err
	}
	limit := supervisorAttachmentCap(kind)
	n, copyErr := io.Copy(out, io.LimitReader(in, limit+1))
	closeErr := out.Close()
	switch {
	case copyErr != nil:
		err = copyErr
	case closeErr != nil:
		err = closeErr
	case n > limit:
		err = supervisorAttachmentCapError(name, kind)
	}
	if err != nil {
		_ = os.Remove(dest)
		return "", 0, err
	}
	return dest, n, nil
}

// writeSupervisorAttachmentError reports an attachment refusal as the
// canonical 400; it returns false for any other error.
func writeSupervisorAttachmentError(w http.ResponseWriter, err error) bool {
	var attachErr *supervisorAttachmentError
	var refErr *uploadReferenceError
	switch {
	case errors.As(err, &attachErr), errors.As(err, &refErr):
		writeAPIError(w, http.StatusBadRequest, errcat.BadRequest, errcat.WithDiagnostics(err.Error()))
		return true
	case errors.Is(err, supervisor.ErrEmptyMessage):
		writeAPIError(w, http.StatusBadRequest, errcat.BadRequest, errcat.WithDiagnostics("text is required unless the message has attachments"))
		return true
	}
	return false
}

// supervisorAttachmentDTOs projects a user record's attachments.
func supervisorAttachmentDTOs(atts []supervisor.Attachment) []SupervisorAttachment {
	if len(atts) == 0 {
		return nil
	}
	out := make([]SupervisorAttachment, 0, len(atts))
	for _, a := range atts {
		out = append(out, SupervisorAttachment{Path: a.Path, Kind: SupervisorAttachmentKind(a.Kind), Name: a.Name, Size: a.Size})
	}
	return out
}
