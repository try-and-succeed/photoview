package face_detection

import (
	"math"
	"testing"

	"github.com/photoview/photoview/api/graphql/models"
)

func constantDescriptor(value float32) models.FaceDescriptor {
	var d models.FaceDescriptor
	for i := range d {
		d[i] = value
	}
	return d
}

// offsetForSquaredDistance returns the per-component offset that puts a
// descriptor at the given squared distance from another: 128 * offset^2.
func offsetForSquaredDistance(squared float64) float32 {
	return float32(math.Sqrt(squared / 128))
}

func TestSquaredDistanceAgainstTolerance(t *testing.T) {
	origin := constantDescriptor(0)

	// 0.2 is compared with the squared distance: about 0.447 Euclidean.
	below := constantDescriptor(offsetForSquaredDistance(0.199))
	above := constantDescriptor(offsetForSquaredDistance(0.201))

	refs := []Reference{{FaceGroupID: 7, Descriptor: origin}}
	if got := ClassifyByVote(refs, below, DefaultTolerance); got != 7 {
		t.Errorf("squared distance 0.199: got %d, want group 7", got)
	}
	if got := ClassifyByVote(refs, above, DefaultTolerance); got != -1 {
		t.Errorf("squared distance 0.201: got %d, want no match", got)
	}

	// A Euclidean distance of 0.4 (squared 0.16) is within; 0.46 (squared
	// 0.2116) is not - which a Euclidean comparison would get wrong both ways.
	if got := ClassifyByVote(refs, constantDescriptor(offsetForSquaredDistance(0.16)), DefaultTolerance); got != 7 {
		t.Errorf("Euclidean 0.4: got %d, want group 7", got)
	}
	if got := ClassifyByVote(refs, constantDescriptor(offsetForSquaredDistance(0.2116)), DefaultTolerance); got != -1 {
		t.Errorf("Euclidean 0.46: got %d, want no match", got)
	}
}

func TestClassifyByVote(t *testing.T) {
	at := func(squared float64) models.FaceDescriptor {
		return constantDescriptor(offsetForSquaredDistance(squared))
	}
	probe := constantDescriptor(0)

	t.Run("the majority of the nearest beats the single nearest", func(t *testing.T) {
		refs := []Reference{
			{1, at(0.01)}, // nearest, alone
			{2, at(0.02)}, {2, at(0.03)},
		}
		if got := ClassifyByVote(refs, probe, DefaultTolerance); got != 2 {
			t.Errorf("got %d, want group 2", got)
		}
	})

	t.Run("a tie goes to the group with the nearest face", func(t *testing.T) {
		refs := []Reference{
			{1, at(0.05)}, {1, at(0.06)},
			{2, at(0.04)}, {2, at(0.07)},
		}
		if got := ClassifyByVote(refs, probe, DefaultTolerance); got != 2 {
			t.Errorf("got %d, want group 2", got)
		}
	})

	t.Run("only the 10 nearest vote", func(t *testing.T) {
		refs := make([]Reference, 0)
		// Group 1: 4 nearest and 5 more beyond the 10th place - 9 in all.
		for i := 0; i < 4; i++ {
			refs = append(refs, Reference{1, at(0.01 + float64(i)*0.001)})
		}
		for i := 0; i < 5; i++ {
			refs = append(refs, Reference{1, at(0.15 + float64(i)*0.001)})
		}
		// Group 2: 6 in places 5 to 10.
		for i := 0; i < 6; i++ {
			refs = append(refs, Reference{2, at(0.05 + float64(i)*0.001)})
		}
		if got := ClassifyByVote(refs, probe, DefaultTolerance); got != 2 {
			t.Errorf("got %d, want group 2 (6 of the 10 nearest)", got)
		}
	})

	t.Run("fewer than 10 within tolerance vote too", func(t *testing.T) {
		refs := []Reference{{3, at(0.1)}, {4, at(0.5)}}
		if got := ClassifyByVote(refs, probe, DefaultTolerance); got != 3 {
			t.Errorf("got %d, want group 3", got)
		}
	})

	t.Run("references beyond tolerance do not vote", func(t *testing.T) {
		refs := []Reference{
			{1, at(0.19)},
			{2, at(0.3)}, {2, at(0.31)}, {2, at(0.32)},
		}
		if got := ClassifyByVote(refs, probe, DefaultTolerance); got != 1 {
			t.Errorf("got %d, want group 1", got)
		}
	})

	t.Run("no references", func(t *testing.T) {
		if got := ClassifyByVote(nil, probe, DefaultTolerance); got != -1 {
			t.Errorf("got %d, want no match", got)
		}
	})
}
