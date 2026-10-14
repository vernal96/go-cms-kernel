package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/jackc/pgx/v5"
	"github.com/vernal96/go-cms-kernel/modules/core/adapters/postgres/mediaoccurrence"
	"github.com/vernal96/go-cms-kernel/modules/core/field"
	"github.com/vernal96/go-cms-kernel/modules/core/media"
	"github.com/vernal96/go-cms-kernel/modules/core/site"
	"github.com/vernal96/go-cms-kernel/modules/mail"
	"github.com/vernal96/go-cms-kernel/security"
)

const mailTemplateOwnerKind = "mail_template"

func replaceStaticAttachmentOccurrences(ctx context.Context, tx pgx.Tx, item mail.Template) error {
	refs := make([]field.Reference, 0, len(item.Attachments))
	for index, attachment := range item.Attachments {
		if attachment.Source != mail.AttachmentStatic || attachment.MediaID == nil || *attachment.MediaID <= 0 {
			continue
		}
		refs = append(refs, field.Reference{Path: []string{strconv.Itoa(index), "media_id"}, Target: field.ReferenceMedia, ID: int64(*attachment.MediaID)})
	}
	return mediaoccurrence.Replace(ctx, tx, media.FileOccurrence{
		OwnerKind: mailTemplateOwnerKind,
		OwnerID:   int64(item.ID),
		SiteID:    int64(item.SiteID),
		Container: "attachments",
	}, refs)
}

func (r *Repository) ClearFileOccurrence(ctx context.Context, actor *security.UserID, ref media.FileOccurrence) (int64, error) {
	if ref.OwnerKind != mailTemplateOwnerKind || ref.OwnerID <= 0 || ref.SiteID <= 0 || ref.Container != "attachments" || len(ref.Path) != 2 || ref.Path[1] != "media_id" {
		return 0, media.ErrFileDeleteConflict
	}
	index, err := strconv.Atoi(ref.Path[0])
	if err != nil || index < 0 || strconv.Itoa(index) != ref.Path[0] {
		return 0, media.ErrFileDeleteConflict
	}
	tx, err := mediaoccurrence.Transaction(ctx, r.connector.Pool())
	if err != nil {
		return 0, err
	}
	var raw []byte
	if err := tx.QueryRow(ctx, `SELECT attachments FROM mail.templates WHERE site_id=$1 AND id=$2 FOR UPDATE`, ref.SiteID, ref.OwnerID).Scan(&raw); err != nil {
		return 0, mapNotFound(err)
	}
	var attachments []mail.AttachmentTemplate
	if err := json.Unmarshal(raw, &attachments); err != nil {
		return 0, fmt.Errorf("decode Mail template attachments before file deletion: %w", err)
	}
	if index >= len(attachments) || attachments[index].Source != mail.AttachmentStatic || attachments[index].MediaID == nil || int64(*attachments[index].MediaID) != int64(ref.MediaID) {
		return 0, media.ErrFileDeleteConflict
	}
	attachments = append(attachments[:index], attachments[index+1:]...)
	encoded, err := json.Marshal(attachments)
	if err != nil {
		return 0, err
	}
	var updatedAt int64
	if err := tx.QueryRow(ctx, `UPDATE mail.templates SET attachments=$3,updated_at=clock_timestamp(),updated_by=$4 WHERE site_id=$1 AND id=$2 RETURNING (extract(epoch FROM updated_at)*1000000000)::bigint`, ref.SiteID, ref.OwnerID, encoded, actor).Scan(&updatedAt); err != nil {
		return 0, mapNotFound(err)
	}
	updated := mail.Template{ID: mail.TemplateID(ref.OwnerID), SiteID: site.ID(ref.SiteID), Attachments: attachments}
	if err := replaceStaticAttachmentOccurrences(ctx, tx, updated); err != nil {
		return 0, err
	}
	return updatedAt, nil
}
