package face_detection

import (
	"sort"

	"github.com/photoview/photoview/api/graphql/models"
)

// DefaultTolerance is the tolerance the detector classifies new faces with.
const DefaultTolerance float32 = 0.2

// maxVotes is how many of the nearest references vote, as in go-face.
const maxVotes = 10

// Reference is a face whose group is taken as given when classifying others.
type Reference struct {
	FaceGroupID int
	Descriptor  models.FaceDescriptor
}

// SquaredDistance is what go-face compares with the tolerance: the squared
// Euclidean distance (dlib::squared_euclidean_distance), summed in float32.
func SquaredDistance(a, b models.FaceDescriptor) float32 {
	var sum float32
	for i := range a {
		d := a[i] - b[i]
		sum += d * d
	}
	return sum
}

// ClassifyByVote decides the group of descriptor the way go-face's
// ClassifyThreshold does, without cgo and without touching the detector's
// state: the references within tolerance (squared distance <= tolerance) are
// sorted by distance, the up to 10 nearest vote by group, and the group with
// most votes wins; a tie goes to the group with the nearest face. It returns
// -1 when no reference is within tolerance.
func ClassifyByVote(references []Reference, descriptor models.FaceDescriptor, tolerance float32) int {
	type hit struct {
		faceGroupID int
		distance    float32
	}

	hits := make([]hit, 0)
	for _, reference := range references {
		distance := SquaredDistance(reference.Descriptor, descriptor)
		if distance <= tolerance {
			hits = append(hits, hit{reference.FaceGroupID, distance})
		}
	}
	if len(hits) == 0 {
		return -1
	}

	sort.SliceStable(hits, func(i, j int) bool { return hits[i].distance < hits[j].distance })
	if len(hits) > maxVotes {
		hits = hits[:maxVotes]
	}

	type tally struct {
		votes   int
		nearest float32
	}
	tallies := make(map[int]*tally)
	order := make([]int, 0)
	for _, h := range hits {
		if t, ok := tallies[h.faceGroupID]; ok {
			t.votes++
			continue
		}
		// Hits are sorted, so the first one of a group is its nearest.
		tallies[h.faceGroupID] = &tally{votes: 1, nearest: h.distance}
		order = append(order, h.faceGroupID)
	}

	best := order[0]
	for _, faceGroupID := range order[1:] {
		t, b := tallies[faceGroupID], tallies[best]
		if t.votes > b.votes || (t.votes == b.votes && t.nearest < b.nearest) {
			best = faceGroupID
		}
	}
	return best
}
