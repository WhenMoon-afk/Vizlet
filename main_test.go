package main

import (
	"path/filepath"
	"sort"
	"testing"
)

func TestNaturalLessSortsNumericRunsByValue(t *testing.T) {
	files := []string{
		"image10.png",
		"image2.png",
		"image1.png",
		"image001.png",
		"image20.png",
		"image3.png",
	}

	sort.Slice(files, func(i, j int) bool { return naturalLess(files[i], files[j]) })

	want := []string{
		"image1.png",
		"image001.png",
		"image2.png",
		"image3.png",
		"image10.png",
		"image20.png",
	}
	if len(files) != len(want) {
		t.Fatalf("got %d files, want %d", len(files), len(want))
	}
	for i := range want {
		if files[i] != want[i] {
			t.Fatalf("sorted files[%d] = %q, want %q; full order: %#v", i, files[i], want[i], files)
		}
	}
}

func TestNaturalPathLessUsesBaseName(t *testing.T) {
	a := filepath.Join("C:\\images", "shot10.png")
	b := filepath.Join("C:\\images", "shot2.png")
	if !naturalPathLess(b, a) {
		t.Fatalf("expected shot2 to sort before shot10")
	}
	if naturalPathLess(a, b) {
		t.Fatalf("did not expect shot10 to sort before shot2")
	}
}

func TestNaturalLessHandlesMultipleNumericRuns(t *testing.T) {
	files := []string{
		"img1-frame10.png",
		"img1-frame2.png",
		"img1-frame1.png",
	}

	sort.Slice(files, func(i, j int) bool { return naturalLess(files[i], files[j]) })

	want := []string{"img1-frame1.png", "img1-frame2.png", "img1-frame10.png"}
	for i := range want {
		if files[i] != want[i] {
			t.Fatalf("sorted files[%d] = %q, want %q; full order: %#v", i, files[i], want[i], files)
		}
	}
}

func TestNaturalLessHandlesZeroPadding(t *testing.T) {
	files := []string{"img00.png", "img000.png", "img0.png"}

	sort.Slice(files, func(i, j int) bool { return naturalLess(files[i], files[j]) })

	want := []string{"img0.png", "img00.png", "img000.png"}
	for i := range want {
		if files[i] != want[i] {
			t.Fatalf("sorted files[%d] = %q, want %q; full order: %#v", i, files[i], want[i], files)
		}
	}
}

func TestNaturalLessOrdersDigitsBeforeLetters(t *testing.T) {
	if !naturalLess("img2.png", "imgA.png") {
		t.Fatalf("expected digit run to sort before letter at the same position")
	}
	if naturalLess("imgA.png", "img2.png") {
		t.Fatalf("did not expect letter to sort before digit run at the same position")
	}
}

func TestNaturalLessIsCaseInsensitive(t *testing.T) {
	if !naturalLess("Frame2.PNG", "frame10.png") {
		t.Fatalf("expected case-insensitive natural sort")
	}
	if naturalLess("Image1.png", "image1.png") || naturalLess("image1.png", "Image1.png") {
		t.Fatalf("case-only differences should compare equivalent")
	}
}
