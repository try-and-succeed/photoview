package resolvers

import (
	"errors"

	"github.com/photoview/photoview/api/graphql/models"
	"github.com/photoview/photoview/api/scanner/face_detection"
	"gorm.io/gorm"
)

type reclassifyOutcome int

const (
	reclassifyKept reclassifyOutcome = iota
	reclassifyMoved
	reclassifyConflict
	reclassifyUnmatched
)

type reclassifyDecision struct {
	face    *models.ImageFace
	outcome reclassifyOutcome
	// The group a moved face goes to.
	destination int
}

// reclassifyFaceGroups assigns the unverified faces of the given groups again,
// with the verified faces on the user's photos, in every group, as the only
// references. The references stay fixed for the whole run, so a face placed
// here never pulls in the next one. Each face is classified the way the
// detector does it (see face_detection.ClassifyByVote).
func reclassifyFaceGroups(db *gorm.DB, user *models.User, faceGroupIDs []int, dryRun bool, tolerance *float64) (*models.ReclassifyResult, error) {
	result := &models.ReclassifyResult{Changes: []*models.FaceReassignment{}}
	if len(faceGroupIDs) == 0 {
		return result, nil
	}

	tol := face_detection.DefaultTolerance
	if tolerance != nil {
		if *tolerance <= 0 {
			return nil, errors.New("the tolerance must be greater than 0")
		}
		tol = float32(*tolerance)
	}

	for _, id := range faceGroupIDs {
		group, err := userOwnedFaceGroup(db, user, id)
		if err != nil {
			return nil, err
		}
		// userOwnedFaceGroup finds with Find, which reports no error when
		// nothing matches; an empty group means not found or not owned.
		if group.ID == 0 {
			return nil, errors.New("face group does not exist or is not owned by the user")
		}
	}

	if err := user.FillAlbums(db); err != nil {
		return nil, err
	}
	userAlbumIDs := make([]int, len(user.Albums))
	for i, album := range user.Albums {
		userAlbumIDs[i] = album.ID
	}

	userFaces := func() *gorm.DB {
		return db.
			Model(&models.ImageFace{}).
			Joins("JOIN media ON media.id = image_faces.media_id").
			Where(mediaAlbumIDInQuestion, userAlbumIDs)
	}

	var verifiedFaces []*models.ImageFace
	if err := userFaces().
		Select("image_faces.id", "image_faces.face_group_id", "image_faces.descriptor").
		Where("image_faces.verified = ?", true).
		Find(&verifiedFaces).Error; err != nil {
		return nil, err
	}
	if len(verifiedFaces) == 0 {
		return nil, errors.New("there are no confirmed faces to compare with yet; confirm some faces first")
	}

	references := make([]face_detection.Reference, len(verifiedFaces))
	for i, face := range verifiedFaces {
		references[i] = face_detection.Reference{FaceGroupID: face.FaceGroupID, Descriptor: face.Descriptor}
	}
	result.VerifiedSamples = len(references)

	var candidates []*models.ImageFace
	if err := userFaces().
		Select("image_faces.*").
		Where("image_faces.face_group_id IN (?)", faceGroupIDs).
		Where("image_faces.verified = ?", false).
		Order("image_faces.id").
		Find(&candidates).Error; err != nil {
		return nil, err
	}
	result.Checked = len(candidates)

	// How many faces each group has on each photo a candidate is on. A face
	// may not move into a group that already shows its photo.
	type groupPhoto struct{ faceGroupID, mediaID int }
	var occupied []struct {
		FaceGroupID int
		MediaID     int
		Faces       int
	}
	if err := db.
		Table("image_faces").
		Select("face_group_id, media_id, COUNT(*) AS faces").
		Where("media_id IN (?)", db.Table("image_faces").Select("media_id").Where("face_group_id IN (?)", faceGroupIDs)).
		Group("face_group_id, media_id").
		Scan(&occupied).Error; err != nil {
		return nil, err
	}
	occupancy := make(map[groupPhoto]int, len(occupied))
	for _, o := range occupied {
		occupancy[groupPhoto{o.FaceGroupID, o.MediaID}] = o.Faces
	}

	decisions := make([]reclassifyDecision, len(candidates))
	for i, face := range candidates {
		decision := reclassifyDecision{face: face}
		match := face_detection.ClassifyByVote(references, face.Descriptor, tol)
		own := groupPhoto{face.FaceGroupID, face.MediaID}

		switch {
		case match < 0:
			decision.outcome = reclassifyUnmatched
			occupancy[own]--
			result.Unmatched++
		case match == face.FaceGroupID:
			decision.outcome = reclassifyKept
			result.Kept++
		case occupancy[groupPhoto{match, face.MediaID}] > 0:
			decision.outcome = reclassifyConflict
			result.Conflicts++
		default:
			decision.outcome = reclassifyMoved
			decision.destination = match
			occupancy[own]--
			occupancy[groupPhoto{match, face.MediaID}]++
			result.Moved++
		}
		decisions[i] = decision
	}

	// The groups the changes refer to, loaded before any of them is emptied.
	groupIDs := make([]int, 0)
	for _, d := range decisions {
		if d.outcome == reclassifyMoved || d.outcome == reclassifyUnmatched {
			groupIDs = append(groupIDs, d.face.FaceGroupID)
		}
		if d.outcome == reclassifyMoved {
			groupIDs = append(groupIDs, d.destination)
		}
	}
	groups := make(map[int]*models.FaceGroup)
	if len(groupIDs) > 0 {
		var loaded []*models.FaceGroup
		if err := db.Where("id IN (?)", groupIDs).Find(&loaded).Error; err != nil {
			return nil, err
		}
		for _, group := range loaded {
			groups[group.ID] = group
		}
	}

	newGroups := make(map[int]*models.FaceGroup)
	if !dryRun {
		if err := applyReclassifyDecisions(db, decisions, faceGroupIDs, newGroups); err != nil {
			return nil, err
		}
		if err := face_detection.GlobalFaceDetector.ReloadFacesFromDatabase(db); err != nil {
			return nil, err
		}
	}

	for _, d := range decisions {
		var to *models.FaceGroup
		switch d.outcome {
		case reclassifyMoved:
			to = groups[d.destination]
		case reclassifyUnmatched:
			to = newGroups[d.face.ID] // nil with dryRun
		default:
			continue
		}

		from := groups[d.face.FaceGroupID]
		face := d.face
		if !dryRun {
			face.FaceGroupID = to.ID
			face.AssignedBy = models.FaceAssignmentReclassify
			face.NeedsReview = true
		}
		result.Changes = append(result.Changes, &models.FaceReassignment{ImageFace: face, From: from, To: to})
	}

	return result, nil
}

// applyReclassifyDecisions writes a run in one transaction. Every face it
// looked at, kept or not, is marked as placed by it and up for review; a face
// without a match moves to a new group of its own. newGroups receives those
// groups by face ID.
func applyReclassifyDecisions(db *gorm.DB, decisions []reclassifyDecision, faceGroupIDs []int, newGroups map[int]*models.FaceGroup) error {
	return db.Transaction(func(tx *gorm.DB) error {
		flags := map[string]any{
			"assigned_by":  models.FaceAssignmentReclassify,
			"needs_review": true,
		}
		withGroup := func(faceGroupID int) map[string]any {
			updates := map[string]any{"face_group_id": faceGroupID}
			for k, v := range flags {
				updates[k] = v
			}
			return updates
		}

		stayingIDs := make([]int, 0)
		for _, d := range decisions {
			switch d.outcome {
			case reclassifyKept, reclassifyConflict:
				stayingIDs = append(stayingIDs, d.face.ID)
			case reclassifyMoved:
				if err := tx.Model(&models.ImageFace{}).Where("id = ?", d.face.ID).Updates(withGroup(d.destination)).Error; err != nil {
					return err
				}
			case reclassifyUnmatched:
				group := &models.FaceGroup{}
				if err := tx.Create(group).Error; err != nil {
					return err
				}
				if err := tx.Model(&models.ImageFace{}).Where("id = ?", d.face.ID).Updates(withGroup(group.ID)).Error; err != nil {
					return err
				}
				newGroups[d.face.ID] = group
			}
		}

		const batchSize = 500
		for start := 0; start < len(stayingIDs); start += batchSize {
			end := min(start+batchSize, len(stayingIDs))
			if err := tx.Model(&models.ImageFace{}).Where("id IN (?)", stayingIDs[start:end]).Updates(flags).Error; err != nil {
				return err
			}
		}

		// The selected groups this emptied.
		return tx.
			Where("id IN (?)", faceGroupIDs).
			Where("NOT EXISTS (?)", tx.Table("image_faces").Select("1").
				Where("image_faces.face_group_id = face_groups.id")).
			Delete(&models.FaceGroup{}).Error
	})
}
