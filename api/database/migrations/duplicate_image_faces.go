package migrations

import (
	"fmt"
	"sort"
	"time"

	"github.com/photoview/photoview/api/graphql/models"
	"gorm.io/gorm"
)

// The scanner used to detect the faces of a photo again on every rescan that
// rebuilt its thumbnails, adding a copy of each face: same photo, same
// rectangle, usually the same group.
type duplicateImageFace struct {
	ID          int
	MediaID     int
	Rectangle   string
	FaceGroupID int
	CreatedAt   time.Time
	UpdatedAt   time.Time
	Label       *string
}

// A face counts as edited by the user when it changed after it was detected,
// e.g. moved to another group. Detection writes both timestamps at once.
const editedAfterDetection = time.Second

// deleteBatchSize stays below SQLite's limit of bound parameters.
const deleteBatchSize = 500

// MigrateForDuplicateImageFaces deletes the copies of faces that repeated
// detection created, keeping one face per photo and rectangle. When copies
// ended up in different groups, the one in a named group stays; among several
// named groups or none, the one the user edited last, else the oldest. Face
// groups that only held copies are deleted too. Running it again changes
// nothing.
func MigrateForDuplicateImageFaces(db *gorm.DB) error {
	return db.Transaction(func(tx *gorm.DB) error {
		duplicateSets := tx.
			Table("image_faces").
			Select("media_id, rectangle").
			Group("media_id, rectangle").
			Having("COUNT(*) > 1")

		var faces []duplicateImageFace
		if err := tx.
			Table("image_faces").
			Select("image_faces.id, image_faces.media_id, image_faces.rectangle, image_faces.face_group_id, "+
				"image_faces.created_at, image_faces.updated_at, face_groups.label").
			Joins("JOIN (?) AS duplicates ON duplicates.media_id = image_faces.media_id "+
				"AND duplicates.rectangle = image_faces.rectangle", duplicateSets).
			Joins("LEFT JOIN face_groups ON face_groups.id = image_faces.face_group_id").
			Order("image_faces.id").
			Scan(&faces).Error; err != nil {
			return fmt.Errorf("failed to find duplicate image faces: %w", err)
		}

		if len(faces) == 0 {
			return nil
		}

		type faceKey struct {
			mediaID   int
			rectangle string
		}
		sets := make(map[faceKey][]duplicateImageFace)
		keys := make([]faceKey, 0)
		for _, face := range faces {
			key := faceKey{face.MediaID, face.Rectangle}
			if _, seen := sets[key]; !seen {
				keys = append(keys, key)
			}
			sets[key] = append(sets[key], face)
		}

		deleteIDs := make([]int, 0, len(faces))
		touchedGroups := make(map[int]bool)
		for _, key := range keys {
			keep := duplicateImageFaceToKeep(sets[key])
			for _, face := range sets[key] {
				if face.ID != keep {
					deleteIDs = append(deleteIDs, face.ID)
					touchedGroups[face.FaceGroupID] = true
				}
			}
		}

		for start := 0; start < len(deleteIDs); start += deleteBatchSize {
			end := min(start+deleteBatchSize, len(deleteIDs))
			if err := tx.Where("id IN ?", deleteIDs[start:end]).Delete(&models.ImageFace{}).Error; err != nil {
				return fmt.Errorf("failed to delete duplicate image faces: %w", err)
			}
		}

		groupIDs := make([]int, 0, len(touchedGroups))
		for id := range touchedGroups {
			groupIDs = append(groupIDs, id)
		}
		sort.Ints(groupIDs)

		// Only groups this run emptied; groups that were empty before are
		// not this migration's business.
		for start := 0; start < len(groupIDs); start += deleteBatchSize {
			end := min(start+deleteBatchSize, len(groupIDs))
			if err := tx.
				Where("id IN ?", groupIDs[start:end]).
				Where("NOT EXISTS (?)", tx.Table("image_faces").Select("1").
					Where("image_faces.face_group_id = face_groups.id")).
				Delete(&models.FaceGroup{}).Error; err != nil {
				return fmt.Errorf("failed to delete face groups left empty: %w", err)
			}
		}

		return nil
	})
}

// duplicateImageFaceToKeep picks the face to keep among copies of one face,
// given in ascending ID order.
func duplicateImageFaceToKeep(copies []duplicateImageFace) int {
	candidates := make([]duplicateImageFace, 0, len(copies))
	for _, face := range copies {
		if face.Label != nil && *face.Label != "" {
			candidates = append(candidates, face)
		}
	}
	if len(candidates) == 0 {
		candidates = copies
	}

	// Within the group the choice falls on, the oldest copy stays.
	keepGroup := candidates[0].FaceGroupID
	var lastEdit time.Time
	for _, face := range candidates {
		if face.UpdatedAt.Sub(face.CreatedAt) > editedAfterDetection && face.UpdatedAt.After(lastEdit) {
			lastEdit = face.UpdatedAt
			keepGroup = face.FaceGroupID
		}
	}

	for _, face := range candidates {
		if face.FaceGroupID == keepGroup {
			return face.ID
		}
	}
	return candidates[0].ID
}
