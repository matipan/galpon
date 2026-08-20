package store

import (
	"context"
	"database/sql"
	"encoding/base64"
	"fmt"
	"net/url"
	"time"

	"github.com/matipan/galpon/internal/model"
)

type imageQueryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

// PutAgentMessageWithImages stores a delivery and all of its image blobs in one
// transaction. A partial delivery is never visible.
func (s *Store) PutAgentMessageWithImages(ctx context.Context, value model.AgentMessage) error {
	value = normalizeAgentMessage(value)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `insert into agent_messages(`+agentMessageColumns+`) values(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, agentMessageValues(value)...); err != nil {
		return err
	}
	if err := putMessageImages(ctx, tx, value.ID, messageImageValues(value.Images), value.CreatedAt); err != nil {
		return err
	}
	return tx.Commit()
}

func messageImageValues(images *[]model.ImageAttachment) []model.ImageAttachment {
	if images == nil {
		return nil
	}
	return *images
}

func imagePointer(images []model.ImageAttachment) *[]model.ImageAttachment {
	if len(images) == 0 {
		return nil
	}
	return &images
}

func putMessageImages(ctx context.Context, tx *sql.Tx, messageID string, images []model.ImageAttachment, createdAt int64) error {
	for position, image := range images {
		if err := putImageBlob(ctx, tx, image, createdAt); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `insert into agent_message_images(message_id,image_id,position) values(?,?,?)`, messageID, image.ID, position); err != nil {
			return err
		}
	}
	return nil
}

func putConversationImages(ctx context.Context, tx *sql.Tx, agentID, eventID string, images []model.ImageAttachment, createdAt int64) error {
	for position, image := range images {
		if err := putImageBlob(ctx, tx, image, createdAt); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `insert into conversation_event_images(agent_id,event_id,image_id,position) values(?,?,?,?)`, agentID, eventID, image.ID, position); err != nil {
			return err
		}
	}
	return nil
}

func putImageBlob(ctx context.Context, tx *sql.Tx, image model.ImageAttachment, createdAt int64) error {
	data, err := base64.StdEncoding.DecodeString(image.Data)
	if err != nil {
		return fmt.Errorf("decode image %s: %w", image.ID, err)
	}
	if int64(len(data)) != image.Size || image.Size <= 0 || image.Size > 8<<20 {
		return fmt.Errorf("image %s has an invalid size", image.ID)
	}
	if createdAt <= 0 {
		createdAt = time.Now().UnixMilli()
	}
	_, err = tx.ExecContext(ctx, `insert into image_blobs(id,mime_type,name,size,width,height,data,created_at) values(?,?,?,?,?,?,?,?)`,
		image.ID, image.MimeType, image.Name, image.Size, image.Width, image.Height, data, createdAt)
	return err
}

func imageURL(id string) string { return "/api/v1/images/" + url.PathEscape(id) }

func loadMessageImages(ctx context.Context, query imageQueryer, messageID string, includeData bool) ([]model.ImageAttachment, error) {
	dataColumn := "x''"
	if includeData {
		dataColumn = "image_blobs.data"
	}
	rows, err := query.QueryContext(ctx, `select image_blobs.id,image_blobs.name,image_blobs.mime_type,image_blobs.size,image_blobs.width,image_blobs.height,`+dataColumn+` from agent_message_images join image_blobs on image_blobs.id=agent_message_images.image_id where agent_message_images.message_id=? order by agent_message_images.position`, messageID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	return scanImages(rows, includeData)
}

func scanImages(rows *sql.Rows, includeData bool) ([]model.ImageAttachment, error) {
	images := []model.ImageAttachment{}
	for rows.Next() {
		var image model.ImageAttachment
		var data []byte
		if err := rows.Scan(&image.ID, &image.Name, &image.MimeType, &image.Size, &image.Width, &image.Height, &data); err != nil {
			return nil, err
		}
		image.URL = imageURL(image.ID)
		if includeData {
			image.Data = base64.StdEncoding.EncodeToString(data)
		}
		images = append(images, image)
	}
	return images, rows.Err()
}

func (s *Store) hydrateMessageImages(ctx context.Context, messages []model.AgentMessage, includeData bool) error {
	for index := range messages {
		images, err := loadMessageImages(ctx, s.db, messages[index].ID, includeData)
		if err != nil {
			return err
		}
		messages[index].Images = imagePointer(images)
	}
	return nil
}

func (s *Store) hydrateConversationImages(ctx context.Context, events []model.ConversationEvent) error {
	for index := range events {
		rows, err := s.db.QueryContext(ctx, `select image_blobs.id,image_blobs.name,image_blobs.mime_type,image_blobs.size,image_blobs.width,image_blobs.height,x'' from conversation_event_images join image_blobs on image_blobs.id=conversation_event_images.image_id where conversation_event_images.agent_id=? and conversation_event_images.event_id=? order by conversation_event_images.position`, events[index].AgentID, events[index].EventID)
		if err != nil {
			return err
		}
		images, scanErr := scanImages(rows, false)
		_ = rows.Close()
		if scanErr != nil {
			return scanErr
		}
		events[index].Images = images
	}
	return nil
}

// PublicImage returns an exact stored blob only when one of its owners is
// visible in the Companion.
func (s *Store) PublicImage(ctx context.Context, id string) (model.ImageAttachment, []byte, error) {
	var image model.ImageAttachment
	var data []byte
	err := s.db.QueryRowContext(ctx, `select image_blobs.id,image_blobs.name,image_blobs.mime_type,image_blobs.size,image_blobs.width,image_blobs.height,image_blobs.data
from image_blobs where image_blobs.id=? and (
 exists (select 1 from agent_message_images join agent_messages on agent_messages.id=agent_message_images.message_id join agents on agents.id=agent_messages.target_agent_id join workstreams on workstreams.id=agents.workstream_id where agent_message_images.image_id=image_blobs.id and workstreams.status='active' and not exists (select 1 from deleted_items where kind='agent' and resource_id=agents.id) and not exists (select 1 from deleted_items where kind='workspace' and resource_id=workstreams.id))
 or exists (select 1 from conversation_event_images join agents on agents.id=conversation_event_images.agent_id join workstreams on workstreams.id=agents.workstream_id where conversation_event_images.image_id=image_blobs.id and workstreams.status='active' and not exists (select 1 from deleted_items where kind='agent' and resource_id=agents.id) and not exists (select 1 from deleted_items where kind='workspace' and resource_id=workstreams.id))
)`, id).Scan(&image.ID, &image.Name, &image.MimeType, &image.Size, &image.Width, &image.Height, &data)
	if err != nil {
		return model.ImageAttachment{}, nil, err
	}
	image.URL = imageURL(image.ID)
	return image, data, nil
}
