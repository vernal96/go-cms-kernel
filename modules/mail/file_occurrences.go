package mail

import (
	"context"
	"fmt"
	"strconv"

	"github.com/vernal96/go-cms-kernel/modules/core/field"
	"github.com/vernal96/go-cms-kernel/modules/core/file"
	"github.com/vernal96/go-cms-kernel/modules/core/media"
	"github.com/vernal96/go-cms-kernel/modules/core/site"
	"github.com/vernal96/go-cms-kernel/permission"
	"github.com/vernal96/go-cms-kernel/security"
)

const staticAttachmentOwnerKind = "mail_template"

func (r *Runtime) FileOccurrenceOwners() []media.FileOccurrenceOwner {
	cleaner, ok := r.service.repository.(FileOccurrenceCleaner)
	if !ok {
		return nil
	}
	return []media.FileOccurrenceOwner{mailTemplateOccurrenceOwner{siteID: r.service.siteID, cleaner: cleaner}}
}

func (r *Runtime) FileOccurrenceKinds() []string {
	return []string{staticAttachmentOwnerKind}
}

func (r *Runtime) ValidateMediaFileOccurrence(ctx context.Context, ref media.FileOccurrence, target file.File) error {
	if ref.OwnerKind != staticAttachmentOwnerKind || ref.SiteID != int64(r.service.siteID) || ref.Container != "attachments" || ref.OwnerID <= 0 {
		return media.ErrInvalidReference
	}
	template, err := r.service.repository.TemplateByID(ctx, r.service.siteID, TemplateID(ref.OwnerID))
	if err != nil {
		return err
	}
	if err := media.ValidateFileOccurrence(ref, staticAttachmentReferences(template.Attachments, r.service.staticAttachmentMedia), target); err != nil {
		return err
	}
	if !field.MediaMatches(r.service.staticAttachmentMedia, target.Storage, target.MIMEType) {
		return fmt.Errorf("%w: static mail attachment violates its disk or MIME constraints", media.ErrInvalidReference)
	}
	return nil
}

func staticAttachmentReferences(attachments []AttachmentTemplate, options field.MediaOptions) []field.Reference {
	result := make([]field.Reference, 0, len(attachments))
	for index, attachment := range attachments {
		if attachment.Source != AttachmentStatic || attachment.MediaID == nil || *attachment.MediaID <= 0 {
			continue
		}
		result = append(result, field.Reference{
			Path:    []string{strconv.Itoa(index), "media_id"},
			Target:  field.ReferenceMedia,
			ID:      int64(*attachment.MediaID),
			Options: options,
		})
	}
	return result
}

type mailTemplateOccurrenceOwner struct {
	siteID  site.ID
	cleaner FileOccurrenceCleaner
}

func (mailTemplateOccurrenceOwner) Kind() string { return staticAttachmentOwnerKind }

func (mailTemplateOccurrenceOwner) UpdatePermission() permission.Code {
	return TemplateUpdatePermission
}

func (owner mailTemplateOccurrenceOwner) ClearFileOccurrence(ctx context.Context, actor *security.UserID, ref media.FileOccurrence) (int64, error) {
	if ref.SiteID != int64(owner.siteID) {
		return 0, media.ErrInvalidReference
	}
	return owner.cleaner.ClearFileOccurrence(ctx, actor, ref)
}
