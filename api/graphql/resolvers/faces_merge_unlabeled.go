package resolvers

import (
	"errors"
	"math"
	"sort"

	"github.com/photoview/photoview/api/graphql/models"
	"github.com/photoview/photoview/api/scanner/face_detection"
	"gorm.io/gorm"
)

// mergeSimilarUnlabeledFaceGroups merges the unlabeled face groups without a
// confirmed face whose faces look like one person. Those faces are compared
// only with each other: each face is classified the way the detector does it
// (face_detection.ClassifyByVote), against all of them but itself. A face
// that votes for another group links its group with that one. Links are taken
// nearest first, and a link that would put two faces of one photo into one
// group is skipped.
//
// Only groups whose faces are all on the user's photos take part, so a run
// never touches a group another user shares.
//
// With dryRun nothing is written. Otherwise the groups are merged in one
// transaction, every face of a merged group is flagged for review, the moved
// ones are marked as placed by a run, and none is confirmed.
func mergeSimilarUnlabeledFaceGroups(db *gorm.DB, user *models.User, dryRun bool, tolerance *float64) ([]*models.FaceGroupMerge, error) {
	tol := face_detection.DefaultTolerance
	if tolerance != nil {
		if *tolerance <= 0 {
			return nil, errors.New("the tolerance must be greater than 0")
		}
		tol = float32(*tolerance)
	}

	if dryRun {
		merges, _, err := planUnlabeledMerges(db, user, tol)
		return merges, err
	}

	var merges []*models.FaceGroupMerge
	err := db.Transaction(func(tx *gorm.DB) error {
		var faceIDsByGroup map[int][]int
		var err error
		merges, faceIDsByGroup, err = planUnlabeledMerges(tx, user, tol)
		if err != nil {
			return err
		}
		return applyUnlabeledMerges(tx, merges, faceIDsByGroup)
	})
	if err != nil {
		return nil, err
	}

	if len(merges) > 0 && face_detection.GlobalFaceDetector != nil {
		if err := face_detection.GlobalFaceDetector.ReloadFacesFromDatabase(db); err != nil {
			return nil, err
		}
	}

	return merges, nil
}

// planUnlabeledMerges works out the merges. It also returns the face IDs of
// every group taking part.
func planUnlabeledMerges(db *gorm.DB, user *models.User, tol float32) ([]*models.FaceGroupMerge, map[int][]int, error) {
	merges := []*models.FaceGroupMerge{}

	verifiedGroups := db.Table("image_faces").Select("face_group_id").Where("verified = ?", true)
	foreignGroups := db.Table("image_faces").
		Select("image_faces.face_group_id").
		Joins("JOIN media ON media.id = image_faces.media_id").
		Where("media.album_id NOT IN (?)", userAlbumIDsSubquery(db, user))

	var faces []*models.ImageFace
	if err := db.
		Model(&models.ImageFace{}).
		Select("image_faces.*").
		Joins("JOIN face_groups ON face_groups.id = image_faces.face_group_id").
		Where("face_groups.label IS NULL").
		Where("image_faces.face_group_id NOT IN (?)", verifiedGroups).
		Where("image_faces.face_group_id NOT IN (?)", foreignGroups).
		Order("image_faces.id").
		Find(&faces).Error; err != nil {
		return nil, nil, err
	}

	faceIDsByGroup := make(map[int][]int)
	facesByGroup := make(map[int][]*models.ImageFace)
	for _, face := range faces {
		faceIDsByGroup[face.FaceGroupID] = append(faceIDsByGroup[face.FaceGroupID], face.ID)
		facesByGroup[face.FaceGroupID] = append(facesByGroup[face.FaceGroupID], face)
	}
	if len(facesByGroup) < 2 {
		return merges, faceIDsByGroup, nil
	}

	links := unlabeledMergeLinks(faces, tol)
	clusters := clusterFaceGroups(facesByGroup, links)
	if len(clusters) == 0 {
		return merges, faceIDsByGroup, nil
	}

	destinationIDs := make([]int, len(clusters))
	for i, cluster := range clusters {
		destinationIDs[i] = cluster[0]
	}
	var destinations []*models.FaceGroup
	if err := db.Where("id IN (?)", destinationIDs).Find(&destinations).Error; err != nil {
		return nil, nil, err
	}
	destinationByID := make(map[int]*models.FaceGroup, len(destinations))
	for _, group := range destinations {
		destinationByID[group.ID] = group
	}

	for _, cluster := range clusters {
		merge := &models.FaceGroupMerge{
			Destination:        destinationByID[cluster[0]],
			SourceFaceGroupIDs: cluster[1:],
			ImageFaces:         []*models.ImageFace{},
		}
		for _, groupID := range cluster {
			merge.ImageFaces = append(merge.ImageFaces, facesByGroup[groupID]...)
		}
		sort.Slice(merge.ImageFaces, func(i, j int) bool { return merge.ImageFaces[i].ID < merge.ImageFaces[j].ID })
		merges = append(merges, merge)
	}

	// The largest merges first, as they save the most work.
	sort.SliceStable(merges, func(i, j int) bool {
		if len(merges[i].ImageFaces) != len(merges[j].ImageFaces) {
			return len(merges[i].ImageFaces) > len(merges[j].ImageFaces)
		}
		return merges[i].Destination.ID < merges[j].Destination.ID
	})

	return merges, faceIDsByGroup, nil
}

type faceGroupLink struct {
	from, to int
	// The squared distance from the voting face to the nearest face of to.
	distance float32
	faceID   int
}

// unlabeledMergeLinks classifies every face against all the others, and
// returns a link for each face that votes for a group other than its own,
// nearest first.
func unlabeledMergeLinks(faces []*models.ImageFace, tol float32) []faceGroupLink {
	references := make([]face_detection.Reference, len(faces))
	for i, face := range faces {
		references[i] = face_detection.Reference{FaceGroupID: face.FaceGroupID, Descriptor: face.Descriptor}
	}

	links := make([]faceGroupLink, 0)
	last := len(references) - 1
	for i, face := range faces {
		// A face does not vote for itself: swap it to the end and leave it
		// out, then put it back.
		references[i], references[last] = references[last], references[i]
		match := face_detection.ClassifyByVote(references[:last], face.Descriptor, tol)
		references[i], references[last] = references[last], references[i]

		if match < 0 || match == face.FaceGroupID {
			continue
		}

		nearest := float32(math.MaxFloat32)
		for _, other := range faces {
			if other.FaceGroupID == match {
				if d := face_detection.SquaredDistance(other.Descriptor, face.Descriptor); d < nearest {
					nearest = d
				}
			}
		}
		links = append(links, faceGroupLink{from: face.FaceGroupID, to: match, distance: nearest, faceID: face.ID})
	}

	sort.SliceStable(links, func(i, j int) bool {
		if links[i].distance != links[j].distance {
			return links[i].distance < links[j].distance
		}
		return links[i].faceID < links[j].faceID
	})
	return links
}

// clusterFaceGroups joins the groups along the links, nearest first, unless
// the two sides share a photo. It returns each cluster of two or more groups
// with the destination first (most faces, then lowest id) and the other
// groups in ascending order.
func clusterFaceGroups(facesByGroup map[int][]*models.ImageFace, links []faceGroupLink) [][]int {
	parent := make(map[int]int, len(facesByGroup))
	photos := make(map[int]map[int]bool, len(facesByGroup))
	for groupID, faces := range facesByGroup {
		parent[groupID] = groupID
		photos[groupID] = make(map[int]bool, len(faces))
		for _, face := range faces {
			photos[groupID][face.MediaID] = true
		}
	}

	var find func(int) int
	find = func(groupID int) int {
		if parent[groupID] != groupID {
			parent[groupID] = find(parent[groupID])
		}
		return parent[groupID]
	}

	for _, link := range links {
		a, b := find(link.from), find(link.to)
		if a == b {
			continue
		}
		if len(photos[a]) < len(photos[b]) {
			a, b = b, a
		}
		shared := false
		for mediaID := range photos[b] {
			if photos[a][mediaID] {
				shared = true
				break
			}
		}
		if shared {
			continue
		}
		for mediaID := range photos[b] {
			photos[a][mediaID] = true
		}
		delete(photos, b)
		parent[b] = a
	}

	members := make(map[int][]int)
	for groupID := range facesByGroup {
		root := find(groupID)
		members[root] = append(members[root], groupID)
	}

	clusters := make([][]int, 0)
	for _, groups := range members {
		if len(groups) < 2 {
			continue
		}
		sort.Slice(groups, func(i, j int) bool {
			fi, fj := len(facesByGroup[groups[i]]), len(facesByGroup[groups[j]])
			if fi != fj {
				return fi > fj
			}
			return groups[i] < groups[j]
		})
		destination := groups[0]
		sources := append([]int(nil), groups[1:]...)
		sort.Ints(sources)
		clusters = append(clusters, append([]int{destination}, sources...))
	}
	sort.Slice(clusters, func(i, j int) bool { return clusters[i][0] < clusters[j][0] })
	return clusters
}

// applyUnlabeledMerges writes the merges and updates the returned faces to
// match.
func applyUnlabeledMerges(tx *gorm.DB, merges []*models.FaceGroupMerge, faceIDsByGroup map[int][]int) error {
	for _, merge := range merges {
		destinationID := merge.Destination.ID

		movedIDs := make([]int, 0)
		for _, groupID := range merge.SourceFaceGroupIDs {
			movedIDs = append(movedIDs, faceIDsByGroup[groupID]...)
		}
		if err := tx.Model(&models.ImageFace{}).Where("id IN (?)", movedIDs).Updates(map[string]any{
			"face_group_id": destinationID,
			"assigned_by":   models.FaceAssignmentReclassify,
			"needs_review":  true,
		}).Error; err != nil {
			return err
		}

		// The faces that were there already are part of the new claim too.
		if err := tx.Model(&models.ImageFace{}).Where("id IN (?)", faceIDsByGroup[destinationID]).
			Update("needs_review", true).Error; err != nil {
			return err
		}

		if err := tx.Where("id IN (?)", merge.SourceFaceGroupIDs).Delete(&models.FaceGroup{}).Error; err != nil {
			return err
		}

		for _, face := range merge.ImageFaces {
			if face.FaceGroupID != destinationID {
				face.FaceGroupID = destinationID
				face.AssignedBy = models.FaceAssignmentReclassify
			}
			face.NeedsReview = true
		}
	}
	return nil
}
