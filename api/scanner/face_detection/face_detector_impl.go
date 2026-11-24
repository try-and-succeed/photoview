//go:build !no_face_detection

package face_detection

import (
	"log"
	"sync"

	"github.com/Kagami/go-face"
	"github.com/photoview/photoview/api/graphql/models"
	"github.com/photoview/photoview/api/scanner/media_encoding"
	"github.com/photoview/photoview/api/utils"
	"github.com/pkg/errors"
	"gorm.io/gorm"
)

type faceDetector struct {
	mutex           sync.Mutex
	rec             *face.Recognizer
	faceDescriptors []face.Descriptor
	faceGroupIDs    []int32
	imageFaceIDs    []int
	// How many samples the recognizer was last given.
	appliedSamples int
}

func InitializeFaceDetector(db *gorm.DB) error {
	if utils.EnvDisableFaceRecognition.GetBool() {
		log.Printf("Face detection disabled (%s=1)\n", utils.EnvDisableFaceRecognition.GetName())
		return nil
	}

	log.Println("Initializing face detector")

	rec, err := face.NewRecognizer(utils.FaceRecognitionModelsPath())
	if err != nil {
		return errors.Wrap(err, "initialize facedetect recognizer")
	}

	faceDescriptors, faceGroupIDs, imageFaceIDs, err := getSamplesFromDatabase(db)
	if err != nil {
		return errors.Wrap(err, "get face detection samples from database")
	}

	detector := &faceDetector{
		rec:             rec,
		faceDescriptors: faceDescriptors,
		faceGroupIDs:    faceGroupIDs,
		imageFaceIDs:    imageFaceIDs,
	}
	detector.mutex.Lock()
	detector.applySamplesLocked()
	detector.mutex.Unlock()

	GlobalFaceDetector = detector

	return nil
}

// applySamplesLocked hands the in-memory samples to the recognizer, which
// otherwise keeps classifying against the set it was last given. The caller
// holds fd.mutex.
func (fd *faceDetector) applySamplesLocked() {
	fd.rec.SetSamples(fd.faceDescriptors, fd.faceGroupIDs)
	fd.appliedSamples = len(fd.faceDescriptors)
}

func getSamplesFromDatabase(db *gorm.DB) (samples []face.Descriptor, faceGroupIDs []int32, imageFaceIDs []int, err error) {

	var imageFaces []*models.ImageFace

	if err = db.Find(&imageFaces).Error; err != nil {
		return
	}

	samples = make([]face.Descriptor, len(imageFaces))
	faceGroupIDs = make([]int32, len(imageFaces))
	imageFaceIDs = make([]int, len(imageFaces))

	for i, imgFace := range imageFaces {
		samples[i] = face.Descriptor(imgFace.Descriptor)
		faceGroupIDs[i] = int32(imgFace.FaceGroupID)
		imageFaceIDs[i] = imgFace.ID
	}

	return
}

// ReloadFacesFromDatabase replaces the in-memory face descriptors with the ones in the database
func (fd *faceDetector) ReloadFacesFromDatabase(db *gorm.DB) error {
	faceDescriptors, faceGroupIDs, imageFaceIDs, err := getSamplesFromDatabase(db)
	if err != nil {
		return err
	}

	fd.mutex.Lock()
	defer fd.mutex.Unlock()

	fd.faceDescriptors = faceDescriptors
	fd.faceGroupIDs = faceGroupIDs
	fd.imageFaceIDs = imageFaceIDs
	fd.applySamplesLocked()

	return nil
}

// DetectFaces finds the faces in the given image and saves them to the database
func (fd *faceDetector) DetectFaces(db *gorm.DB, media *models.Media) error {
	if err := db.Model(media).Preload("MediaURL").First(&media).Error; err != nil {
		return err
	}

	// A rescan that rebuilds the thumbnails calls this again for the same
	// photo. A face the photo has already is not added a second time; deleting
	// the old faces first would throw away how the user sorted them. Detection
	// on the same thumbnail finds the same rectangle, so the saved rectangle
	// identifies a face. Faces that an earlier run failed to save are still
	// added.
	var savedRectangles []string
	if err := db.Model(&models.ImageFace{}).Where("media_id = ?", media.ID).Pluck("rectangle", &savedRectangles).Error; err != nil {
		return err
	}
	saved := make(map[string]bool, len(savedRectangles))
	for _, rectangle := range savedRectangles {
		saved[rectangle] = true
	}

	var thumbnailURL *models.MediaURL
	for _, url := range media.MediaURL {
		if url.Purpose == models.PhotoThumbnail {
			thumbnailURL = &url
			thumbnailURL.Media = media
			break
		}
	}

	if thumbnailURL == nil {
		return errors.New("thumbnail url is missing")
	}

	thumbnailPath, err := thumbnailURL.CachedPath()
	if err != nil {
		return err
	}

	fd.mutex.Lock()
	faces, err := fd.rec.RecognizeFile(thumbnailPath)
	fd.mutex.Unlock()

	if err != nil {
		return errors.Wrap(err, "error read faces")
	}

	// A face that fails to save does not stop the others; the next detection
	// on this photo tries it again.
	var saveErr error
	for _, face := range faces {
		if err := fd.classifyFace(db, &face, media, thumbnailPath, saved); err != nil && saveErr == nil {
			saveErr = errors.Wrap(err, "error saving face")
		}
	}

	return saveErr
}

func (fd *faceDetector) classifyDescriptor(descriptor face.Descriptor) int32 {
	// go-face ignores an empty sample set and keeps the previous one, so the
	// recognizer cannot be told that there is nothing to match against.
	if fd.appliedSamples == 0 {
		return -1
	}
	return int32(fd.rec.ClassifyThreshold(descriptor, 0.2))
}

// classifyFace saves a detected face in the group it matches, or in a new one.
// A face whose rectangle is in saved is on the photo already and is skipped.
func (fd *faceDetector) classifyFace(db *gorm.DB, face *face.Face, media *models.Media, imagePath string, saved map[string]bool) error {
	fd.mutex.Lock()
	defer fd.mutex.Unlock()

	match := fd.classifyDescriptor(face.Descriptor)

	dimension, err := media_encoding.GetPhotoDimensions(imagePath)
	if err != nil {
		return err
	}

	imageFace := models.ImageFace{
		MediaID:    media.ID,
		Descriptor: models.FaceDescriptor(face.Descriptor),
		Rectangle: models.FaceRectangle{
			// Converts a pixel absolute rectangle to a relative FaceRectangle.
			MinX: float64(face.Rectangle.Min.X) / float64(dimension.Width),
			MaxX: float64(face.Rectangle.Max.X) / float64(dimension.Width),
			MinY: float64(face.Rectangle.Min.Y) / float64(dimension.Height),
			MaxY: float64(face.Rectangle.Max.Y) / float64(dimension.Height),
		},
	}

	// Compared in the form it is stored in; Value never fails.
	rectangle, _ := imageFace.Rectangle.Value()
	if key, ok := rectangle.(string); ok && saved[key] {
		return nil
	}

	var faceGroup models.FaceGroup

	// If no match add it new to samples
	if match < 0 {
		log.Println("No match, assigning new face")

		faceGroup = models.FaceGroup{
			ImageFaces: []models.ImageFace{imageFace},
		}

		if err := db.Create(&faceGroup).Error; err != nil {
			return err
		}

	} else {
		log.Println("Found match")

		if err := db.First(&faceGroup, int(match)).Error; err != nil {
			return err
		}

		if err := db.Model(&faceGroup).Association("ImageFaces").Append(&imageFace); err != nil {
			return err
		}
	}

	fd.faceDescriptors = append(fd.faceDescriptors, face.Descriptor)
	fd.faceGroupIDs = append(fd.faceGroupIDs, int32(faceGroup.ID))
	fd.imageFaceIDs = append(fd.imageFaceIDs, imageFace.ID)

	fd.applySamplesLocked()
	return nil
}

func (fd *faceDetector) MergeCategories(sourceID int32, destID int32) {
	fd.mutex.Lock()
	defer fd.mutex.Unlock()

	for i := range fd.faceGroupIDs {
		if fd.faceGroupIDs[i] == sourceID {
			fd.faceGroupIDs[i] = destID
		}
	}
	fd.applySamplesLocked()
}

func (fd *faceDetector) MergeImageFaces(imageFaceIDs []int, destFaceGroupID int32) {
	fd.mutex.Lock()
	defer fd.mutex.Unlock()

	for i := range fd.faceGroupIDs {
		imageFaceID := fd.imageFaceIDs[i]

		for _, id := range imageFaceIDs {
			if imageFaceID == id {
				fd.faceGroupIDs[i] = destFaceGroupID
				break
			}
		}
	}
	fd.applySamplesLocked()
}

func (fd *faceDetector) RecognizeUnlabeledFaces(tx *gorm.DB, user *models.User) ([]*models.ImageFace, error) {
	var unlabeledFaceGroups []*models.FaceGroup

	err := tx.
		Joins("JOIN image_faces ON image_faces.face_group_id = face_groups.id").
		Joins("JOIN media ON image_faces.media_id = media.id").
		Where("face_groups.label IS NULL").
		Where("media.album_id IN (?)",
			tx.Select("album_id").Table("user_albums").Where("user_id = ?", user.ID),
		).
		Find(&unlabeledFaceGroups).Error

	if err != nil {
		return nil, err
	}

	// A face a person confirmed is never moved automatically.
	var verifiedFaceIDs []int
	if err := tx.Model(&models.ImageFace{}).Where("verified = ?", true).Pluck("id", &verifiedFaceIDs).Error; err != nil {
		return nil, err
	}
	verified := make(map[int]bool, len(verifiedFaceIDs))
	for _, id := range verifiedFaceIDs {
		verified[id] = true
	}

	// Which groups are on the photos of the unlabeled faces already. A run
	// does not put a second face of a person on one photo; a person can be
	// there twice, but only a user can tell, so such a face is flagged.
	type groupPhoto struct{ faceGroupID, mediaID int }
	var onPhotos []struct{ ID, FaceGroupID, MediaID int }
	if err := tx.
		Table("image_faces").
		Select("id, face_group_id, media_id").
		Where("media_id IN (?)", tx.
			Table("image_faces").
			Select("image_faces.media_id").
			Joins("JOIN face_groups ON face_groups.id = image_faces.face_group_id").
			Where("face_groups.label IS NULL")).
		Scan(&onPhotos).Error; err != nil {
		return nil, err
	}
	mediaOf := make(map[int]int, len(onPhotos))
	occupied := make(map[groupPhoto]bool, len(onPhotos))
	for _, face := range onPhotos {
		mediaOf[face.ID] = face.MediaID
		occupied[groupPhoto{face.FaceGroupID, face.MediaID}] = true
	}

	// Labeled means named, whoever's albums the group is on.
	var labeledFaceGroupIDs []int
	if err := tx.Model(&models.FaceGroup{}).Where("label IS NOT NULL").Pluck("id", &labeledFaceGroupIDs).Error; err != nil {
		return nil, err
	}
	labeled := make(map[int32]bool, len(labeledFaceGroupIDs))
	for _, id := range labeledFaceGroupIDs {
		labeled[int32(id)] = true
	}
	candidateGroups := make(map[int32]bool, len(unlabeledFaceGroups))
	for _, faceGroup := range unlabeledFaceGroups {
		candidateGroups[int32(faceGroup.ID)] = true
	}

	fd.mutex.Lock()
	defer fd.mutex.Unlock()

	// A person is a group with a name, or one with a face a user confirmed:
	// confirming a face without naming it says "this is someone, even if I
	// do not know who".
	person := make(map[int32]bool, len(labeled))
	for id := range labeled {
		person[id] = true
	}
	for i, imageFaceID := range fd.imageFaceIDs {
		if verified[imageFaceID] {
			person[fd.faceGroupIDs[i]] = true
		}
	}

	// The references, fixed for the whole run so that a face placed here
	// does not pull in the next one:
	//
	//   - of a person, only the faces a user confirmed. A face the detector
	//     or an earlier run placed may be wrong, and as a reference one wrong
	//     face pulls in more strangers with every run.
	//   - every other unlabeled face. They cannot win a face for themselves,
	//     but they vote: a stranger whose nearest faces are other photos of
	//     that stranger stays where it is, even when a confirmed face of a
	//     person is within the tolerance too. Without them, a person with
	//     only a few confirmed faces gets every similar stranger, which
	//     happens especially with children.
	references := make([]Reference, 0, len(fd.faceDescriptors))
	referenceIndex := make(map[int]int)
	candidates := make([]int, 0)
	for i := range fd.faceDescriptors {
		faceGroupID := fd.faceGroupIDs[i]
		imageFaceID := fd.imageFaceIDs[i]

		if person[faceGroupID] {
			if verified[imageFaceID] {
				references = append(references, Reference{FaceGroupID: int(faceGroupID), Descriptor: models.FaceDescriptor(fd.faceDescriptors[i])})
			} else if candidateGroups[faceGroupID] {
				// An unconfirmed face of an unnamed person: no reference,
				// but it may find a better match.
				candidates = append(candidates, i)
			}
			continue
		}

		referenceIndex[imageFaceID] = len(references)
		references = append(references, Reference{FaceGroupID: int(faceGroupID), Descriptor: models.FaceDescriptor(fd.faceDescriptors[i])})

		// A face a person confirmed is never moved automatically.
		if candidateGroups[faceGroupID] && !verified[imageFaceID] {
			candidates = append(candidates, i)
		}
	}

	updatedImageFaces := make([]*models.ImageFace, 0)
	sourceFaceGroupIDs := make(map[int32]bool)

	last := len(references) - 1
	for _, i := range candidates {
		faceGroupID := fd.faceGroupIDs[i]
		imageFaceID := fd.imageFaceIDs[i]

		var match int
		if self, ok := referenceIndex[imageFaceID]; ok {
			// A face does not vote for itself: swap it to the end and leave
			// it out, then put it back.
			references[self], references[last] = references[last], references[self]
			match = ClassifyByVote(references[:last], models.FaceDescriptor(fd.faceDescriptors[i]), DefaultTolerance)
			references[self], references[last] = references[last], references[self]
		} else {
			match = ClassifyByVote(references, models.FaceDescriptor(fd.faceDescriptors[i]), DefaultTolerance)
		}

		// Only a person wins a face, and a face that matches its own group
		// stays as it is.
		if match < 0 || !person[int32(match)] || int32(match) == faceGroupID {
			continue
		}

		if occupied[groupPhoto{match, mediaOf[imageFaceID]}] {
			// The person is on this photo already: leave the face where it
			// is, up for review.
			if err := tx.Model(&models.ImageFace{}).Where("id = ?", imageFaceID).Updates(map[string]any{
				"assigned_by":  models.FaceAssignmentReclassify,
				"needs_review": true,
			}).Error; err != nil {
				return nil, err
			}
			continue
		}

		var imageFace models.ImageFace
		if err := tx.First(&imageFace, imageFaceID).Error; err != nil {
			return nil, err
		}

		// Placed by a run, so up for review.
		if err := tx.Model(&imageFace).Updates(map[string]any{
			"face_group_id": match,
			"assigned_by":   models.FaceAssignmentReclassify,
			"needs_review":  true,
		}).Error; err != nil {
			return nil, err
		}

		updatedImageFaces = append(updatedImageFaces, &imageFace)
		sourceFaceGroupIDs[faceGroupID] = true
		occupied[groupPhoto{match, imageFace.MediaID}] = true
	}

	// The recognizer that classifies newly scanned faces sees the outcome.
	if len(updatedImageFaces) > 0 {
		moved := make(map[int]int32, len(updatedImageFaces))
		for _, imageFace := range updatedImageFaces {
			moved[imageFace.ID] = int32(imageFace.FaceGroupID)
		}
		for i, imageFaceID := range fd.imageFaceIDs {
			if group, ok := moved[imageFaceID]; ok {
				fd.faceGroupIDs[i] = group
			}
		}
		fd.applySamplesLocked()
	}

	// The unlabeled groups whose faces all found their person are empty now.
	for faceGroupID := range sourceFaceGroupIDs {
		if err := tx.
			Where("id = ?", faceGroupID).
			Where("NOT EXISTS (?)", tx.Table("image_faces").Select("1").
				Where("image_faces.face_group_id = face_groups.id")).
			Delete(&models.FaceGroup{}).Error; err != nil {
			return nil, err
		}
	}

	return updatedImageFaces, nil
}
