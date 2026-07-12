package v4l2

import (
	"errors"
	"os"
	"strings"
	"testing"
)

type fakeSystem struct {
	exists   map[string]bool
	canWrite map[string]bool
	files    map[string][]byte
}

func (f fakeSystem) Exists(path string) bool {
	return f.exists[path]
}

func (f fakeSystem) CanWrite(path string) bool {
	return f.canWrite[path]
}

func (f fakeSystem) ReadFile(path string) ([]byte, error) {
	data, ok := f.files[path]
	if !ok {
		return nil, os.ErrNotExist
	}
	return data, nil
}

func TestVerify(t *testing.T) {
	tests := []struct {
		name    string
		device  string
		sys     fakeSystem
		wantErr bool
		want    []string // substrings expected in the error
	}{
		{
			name:    "invalid device path",
			device:  "/dev/videoX",
			sys:     fakeSystem{},
			wantErr: true,
			want:    []string{"invalid v4l2 device", "/dev/videoX"},
		},
		{
			name:    "empty device path",
			device:  "",
			sys:     fakeSystem{},
			wantErr: true,
			want:    []string{"invalid v4l2 device"},
		},
		{
			name:    "non-video path",
			device:  "/dev/null",
			sys:     fakeSystem{},
			wantErr: true,
			want:    []string{"invalid v4l2 device"},
		},
		{
			name:    "device missing",
			device:  "/dev/video10",
			sys:     fakeSystem{},
			wantErr: true,
			want:    []string{"does not exist", "video_nr=10", "card_label=PhoneCam", "/dev/video10"},
		},
		{
			name:    "device missing custom number",
			device:  "/dev/video7",
			sys:     fakeSystem{},
			wantErr: true,
			want:    []string{"does not exist", "video_nr=7", "/dev/video7"},
		},
		{
			name:   "wrong card label",
			device: "/dev/video10",
			sys: fakeSystem{
				exists: map[string]bool{"/dev/video10": true},
				files:  map[string][]byte{"/sys/class/video4linux/video10/name": []byte("Integrated Camera\n")},
			},
			wantErr: true,
			want:    []string{"belongs to another camera", "Integrated Camera", "card_label=PhoneCam"},
		},
		{
			name:   "wrong card label custom number reads correct sysfs path",
			device: "/dev/video7",
			sys: fakeSystem{
				exists: map[string]bool{"/dev/video7": true},
				files:  map[string][]byte{"/sys/class/video4linux/video7/name": []byte("Some Webcam")},
			},
			wantErr: true,
			want:    []string{"belongs to another camera", "Some Webcam"},
		},
		{
			name:   "not writable",
			device: "/dev/video10",
			sys: fakeSystem{
				exists: map[string]bool{"/dev/video10": true},
				files:  map[string][]byte{"/sys/class/video4linux/video10/name": []byte("PhoneCam\n")},
			},
			wantErr: true,
			want:    []string{"not writable", "video group"},
		},
		{
			name:   "not writable without sysfs skips name check",
			device: "/dev/video10",
			sys: fakeSystem{
				exists: map[string]bool{"/dev/video10": true},
			},
			wantErr: true,
			want:    []string{"not writable"},
		},
		{
			name:   "success with matching label",
			device: "/dev/video10",
			sys: fakeSystem{
				exists:   map[string]bool{"/dev/video10": true},
				canWrite: map[string]bool{"/dev/video10": true},
				files:    map[string][]byte{"/sys/class/video4linux/video10/name": []byte("PhoneCam\n")},
			},
			wantErr: false,
		},
		{
			name:   "success without sysfs skips name check",
			device: "/dev/video10",
			sys: fakeSystem{
				exists:   map[string]bool{"/dev/video10": true},
				canWrite: map[string]bool{"/dev/video10": true},
			},
			wantErr: false,
		},
		{
			name:   "success with custom device number",
			device: "/dev/video7",
			sys: fakeSystem{
				exists:   map[string]bool{"/dev/video7": true},
				canWrite: map[string]bool{"/dev/video7": true},
				files:    map[string][]byte{"/sys/class/video4linux/video7/name": []byte("PhoneCam")},
			},
			wantErr: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := Verify(tc.sys, tc.device)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got nil")
				}
				for _, want := range tc.want {
					if !strings.Contains(err.Error(), want) {
						t.Fatalf("expected error to contain %q, got: %v", want, err)
					}
				}
				return
			}
			if err != nil {
				t.Fatalf("expected no error, got: %v", err)
			}
		})
	}
}

func TestVerifyNilSystemDefaultsToOSSystem(t *testing.T) {
	// A missing device with a nil System must default to OSSystem{} and report
	// that the device does not exist rather than panicking.
	err := Verify(nil, "/dev/video9999")
	if err == nil {
		t.Fatal("expected error for nonexistent device")
	}
	if !strings.Contains(err.Error(), "does not exist") {
		t.Fatalf("expected does-not-exist error, got: %v", err)
	}
}

func TestVerifyReadFileErrorSkipsNameCheck(t *testing.T) {
	// An explicit read error (not just missing map entry) must be treated as
	// "skip the name check", not a failure.
	sys := errReadSystem{fakeSystem: fakeSystem{
		exists:   map[string]bool{"/dev/video10": true},
		canWrite: map[string]bool{"/dev/video10": true},
	}}
	if err := Verify(sys, "/dev/video10"); err != nil {
		t.Fatalf("expected sysfs read error to be skipped, got: %v", err)
	}
}

type errReadSystem struct {
	fakeSystem
}

func (errReadSystem) ReadFile(path string) ([]byte, error) {
	return nil, errors.New("permission denied")
}
