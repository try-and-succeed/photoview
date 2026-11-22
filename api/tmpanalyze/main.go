package main

import (
	"bytes"
	"database/sql"
	"encoding/binary"
	"fmt"
	"sort"
	"sync"

	_ "github.com/go-sql-driver/mysql"
	fd "github.com/photoview/photoview/api/scanner/face_detection"
	"github.com/photoview/photoview/api/graphql/models"
)

type face struct {
	id, group int
	verified  bool
	d         models.FaceDescriptor
}

func main() {
	db, err := sql.Open("mysql", "photoview:photosecret@tcp(127.0.0.1:3307)/photoview_test")
	if err != nil {
		panic(err)
	}
	labels := map[int]string{}
	rows, _ := db.Query("select id, coalesce(label,'') from face_groups")
	for rows.Next() {
		var id int
		var l string
		rows.Scan(&id, &l)
		labels[id] = l
	}
	rows, _ = db.Query("select id, face_group_id, verified, descriptor from image_faces")
	var labeled, unlabeled []*face
	for rows.Next() {
		f := &face{}
		var b []byte
		rows.Scan(&f.id, &f.group, &f.verified, &b)
		binary.Read(bytes.NewReader(b), binary.LittleEndian, &f.d)
		if labels[f.group] != "" {
			labeled = append(labeled, f)
		} else {
			unlabeled = append(unlabeled, f)
		}
	}
	nv := 0
	perVer := map[int]int{}
	perAll := map[int]int{}
	for _, f := range labeled {
		perAll[f.group]++
		if f.verified {
			nv++
			perVer[f.group]++
		}
	}
	fmt.Printf("labeled %d (verified %d), unlabeled %d\n", len(labeled), nv, len(unlabeled))

	refs := func(onlyVerified, withUnlabeled bool) []fd.Reference {
		var r []fd.Reference
		for _, f := range labeled {
			if !onlyVerified || f.verified {
				r = append(r, fd.Reference{FaceGroupID: f.group, Descriptor: f.d})
			}
		}
		if withUnlabeled {
			for _, f := range unlabeled {
				r = append(r, fd.Reference{FaceGroupID: f.group, Descriptor: f.d})
			}
		}
		return r
	}
	variants := []struct {
		name               string
		onlyVer, withUnlab bool
	}{
		{"heute: alle benannten, 0.2", false, false},
		{"nur verifizierte", true, false},
		{"Fremde stimmen mit", false, true},
		{"verifiziert + Fremde", true, true},
	}
	results := make([]map[int]int, len(variants))
	for vi, v := range variants {
		r := refs(v.onlyVer, v.withUnlab)
		res := map[int]int{}
		var mu sync.Mutex
		var wg sync.WaitGroup
		ch := make(chan *face)
		for w := 0; w < 4; w++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for f := range ch {
					var rr []fd.Reference
					if v.withUnlab {
						// a face never votes for itself
						rr = r
					} else {
						rr = r
					}
					g := classify(rr, f, v.withUnlab)
					if g >= 0 && labels[g] != "" {
						mu.Lock()
						res[g]++
						mu.Unlock()
					}
				}
			}()
		}
		for _, f := range unlabeled {
			ch <- f
		}
		close(ch)
		wg.Wait()
		results[vi] = res
		tot := 0
		for _, n := range res {
			tot += n
		}
		fmt.Printf("%-28s -> %d unbenannte Gesichter würden verschoben\n", v.name, tot)
	}
	var gs []int
	for g := range results[0] {
		gs = append(gs, g)
	}
	sort.Slice(gs, func(i, j int) bool { return results[0][gs[i]] > results[0][gs[j]] })
	fmt.Printf("%-22s %6s %6s | %6s %6s %6s %6s\n", "person", "benannt", "verif", "heute", "nurVer", "Fremde", "beides")
	for i, g := range gs {
		if i >= 25 {
			break
		}
		fmt.Printf("%-22s %6d %6d | %6d %6d %6d %6d\n", labels[g], perAll[g], perVer[g], results[0][g], results[1][g], results[2][g], results[3][g])
	}
}

// classify skips the face itself when the unlabeled faces vote too.
func classify(r []fd.Reference, f *face, skipSelf bool) int {
	if !skipSelf {
		return fd.ClassifyByVote(r, f.d, fd.DefaultTolerance)
	}
	// exact self match has distance 0; drop one zero-distance hit of its own group
	var own []fd.Reference
	removed := false
	for _, x := range r {
		if !removed && x.FaceGroupID == f.group && fd.SquaredDistance(x.Descriptor, f.d) == 0 {
			removed = true
			continue
		}
		own = append(own, x)
	}
	return fd.ClassifyByVote(own, f.d, fd.DefaultTolerance)
}
