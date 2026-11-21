package models

import (
	"bytes"
	"database/sql/driver"
	"encoding/binary"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/photoview/photoview/api/database/drivers"
	"gorm.io/gorm"
	"gorm.io/gorm/schema"
)

type FaceGroup struct {
	Model
	Label      *string
	ImageFaces []ImageFace `gorm:"constraint:OnDelete:CASCADE;"`
}

type ImageFace struct {
	Model
	FaceGroupID int `gorm:"not null;index"`
	FaceGroup   *FaceGroup
	MediaID     int            `gorm:"not null;index"`
	Media       Media          `gorm:"constraint:OnDelete:CASCADE;"`
	Descriptor  FaceDescriptor `gorm:"not null"`
	Rectangle   FaceRectangle  `gorm:"not null"`

	// Confirmed by a person: never moved by an automatic run.
	Verified bool `gorm:"not null;default:false;index"`
	// Who put the face into its current group.
	AssignedBy FaceAssignment `gorm:"type:varchar(16);not null;default:'DETECTOR'"`
	// Placed or checked by an automatic run and not looked at yet.
	NeedsReview bool `gorm:"not null;default:false;index"`
}

// FaceAssignment tells who put a face into its current group. The values are
// the GraphQL enum's spelling, so it binds directly.
type FaceAssignment string

const (
	FaceAssignmentDetector   FaceAssignment = "DETECTOR"
	FaceAssignmentUser       FaceAssignment = "USER"
	FaceAssignmentReclassify FaceAssignment = "RECLASSIFY"
)

func (a FaceAssignment) IsValid() bool {
	switch a {
	case FaceAssignmentDetector, FaceAssignmentUser, FaceAssignmentReclassify:
		return true
	}
	return false
}

func (a FaceAssignment) String() string {
	return string(a)
}

func (a *FaceAssignment) UnmarshalGQL(v any) error {
	str, ok := v.(string)
	if !ok {
		return fmt.Errorf("enums must be strings")
	}

	*a = FaceAssignment(str)
	if !a.IsValid() {
		return fmt.Errorf("%s is not a valid FaceAssignment", str)
	}
	return nil
}

func (a FaceAssignment) MarshalGQL(w io.Writer) {
	fmt.Fprint(w, strconv.Quote(a.String()))
}

func (f *ImageFace) FillMedia(db *gorm.DB) error {
	if f.Media.ID != 0 {
		// media already exists
		return nil
	}

	if err := db.Model(&f).Association("Media").Find(&f.Media); err != nil {
		return err
	}

	return nil
}

type FaceDescriptor [128]float32 // same as go-face's Descriptor

// GormDataType datatype used in database
func (FaceDescriptor) GormDBDataType(db *gorm.DB, field *schema.Field) string {
	switch drivers.GetDatabaseDriverType(db) {
	case drivers.MYSQL, drivers.SQLITE:
		return "BLOB"
	case drivers.POSTGRES:
		return "BYTEA"
	}
	return ""
}

// Scan tells GORM how to convert database data to Go format
func (fd *FaceDescriptor) Scan(value interface{}) error {
	byteValue := value.([]byte)
	reader := bytes.NewReader(byteValue)
	binary.Read(reader, binary.LittleEndian, fd)
	return nil
}

// Value tells GORM how to save into the database
func (fd FaceDescriptor) Value() (driver.Value, error) {
	buf := new(bytes.Buffer)
	if err := binary.Write(buf, binary.LittleEndian, fd); err != nil {
		return nil, err
	}

	return buf.Bytes(), nil
}

// FaceRectangle stores a relative rectangle of a face in an image.
type FaceRectangle struct {
	MinX, MaxX float64
	MinY, MaxY float64
}

// GormDataType datatype used in database
func (fr FaceRectangle) GormDataType() string {
	return "VARCHAR(64)"
}

// Scan tells GORM how to convert database data to Go format
func (fr *FaceRectangle) Scan(value interface{}) error {
	stringArray, ok := value.(string)
	if !ok {
		byteArray := value.([]uint8)
		stringArray = string(byteArray)
	}

	slices := strings.Split(stringArray, ":")

	if len(slices) != 4 {
		return fmt.Errorf("invalid face rectangle format, expected 4 values, got %d", len(slices))
	}

	var err error

	fr.MinX, err = strconv.ParseFloat(slices[0], 32)
	if err != nil {
		return err
	}

	fr.MaxX, err = strconv.ParseFloat(slices[1], 32)
	if err != nil {
		return err
	}

	fr.MinY, err = strconv.ParseFloat(slices[2], 32)
	if err != nil {
		return err
	}

	fr.MaxY, err = strconv.ParseFloat(slices[3], 32)
	if err != nil {
		return err
	}

	return nil
}

// Value tells GORM how to save into the database
func (fr FaceRectangle) Value() (driver.Value, error) {
	result := fmt.Sprintf("%f:%f:%f:%f", fr.MinX, fr.MaxX, fr.MinY, fr.MaxY)
	return result, nil
}
