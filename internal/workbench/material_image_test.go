package workbench

import (
	"bytes"
	"encoding/binary"
	"hash/crc32"
	"image"
	"image/jpeg"
	"image/png"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestMaterialsImagePreviewKeepsSourceVersion(t *testing.T) {
	ctx := t.Context()
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	createProject := func(title string) string {
		t.Helper()
		project, _, err := store.createProject(ctx, createProjectInput{RequestID: title, Title: title, TemplateID: "freeform"})
		if err != nil {
			t.Fatal(err)
		}
		return project.ProjectID
	}
	projectID, otherID := createProject("观测图片"), createProject("对照项目")
	var original, newer, photo bytes.Buffer
	if err := png.Encode(&original, image.NewGray(image.Rect(0, 0, 12, 6))); err != nil {
		t.Fatal(err)
	}
	if err := png.Encode(&newer, image.NewGray(image.Rect(0, 0, 24, 12))); err != nil {
		t.Fatal(err)
	}
	if err := jpeg.Encode(&photo, image.NewGray(image.Rect(0, 0, 10, 8)), nil); err != nil {
		t.Fatal(err)
	}
	save := func(name string, data []byte) Material {
		t.Helper()
		material, _, err := store.saveMaterial(ctx, projectID, name, bytes.NewReader(data))
		if err != nil {
			t.Fatal(err)
		}
		return material
	}
	first := save("测站.PNG", original.Bytes())
	second := save("测站.PNG", newer.Bytes())
	jpegImage := save("测站.jpg", photo.Bytes())
	invalid := save("损坏.png", append(bytes.Clone(original.Bytes()), []byte("extra")...))

	// Insert an animation-control chunk after the IHDR. The static decoder
	// ignores this chunk, while browsers can use it to enable APNG playback.
	var animation [20]byte
	binary.BigEndian.PutUint32(animation[:4], 8)
	copy(animation[4:8], "acTL")
	binary.BigEndian.PutUint32(animation[8:12], 1)
	binary.BigEndian.PutUint32(animation[16:], crc32.ChecksumIEEE(animation[4:16]))
	const ihdrEnd = 8 + 12 + 13
	animated := save("动画.png", bytes.Join([][]byte{original.Bytes()[:ihdrEnd], animation[:], original.Bytes()[ihdrEnd:]}, nil))
	handler := NewHandler(store, nil, HandlerConfig{})
	request := func(project string, material Material, suffix string, status int) *httptest.ResponseRecorder {
		t.Helper()
		url := "http://127.0.0.1" + projectsPath + "/" + project + "/materials/" + material.MaterialID + suffix
		res := httptest.NewRecorder()
		handler.ServeHTTP(res, httptest.NewRequestWithContext(ctx, http.MethodGet, url, nil))
		if res.Code != status {
			t.Fatalf("%s: got HTTP %d, want %d", suffix, res.Code, status)
		}
		return res
	}
	for _, source := range []struct {
		material Material
		want     []byte
		mimeType string
	}{{first, original.Bytes(), "image/png"}, {second, newer.Bytes(), "image/png"}, {jpegImage, photo.Bytes(), "image/jpeg"}} {
		preview := request(projectID, source.material, "/image-preview", http.StatusOK)
		download := request(projectID, source.material, "/download", http.StatusOK)
		if preview.Header().Get("Content-Type") != source.mimeType || preview.Header().Get("X-Source-SHA256") != source.material.SHA256 ||
			!bytes.Equal(preview.Body.Bytes(), source.want) || !bytes.Equal(preview.Body.Bytes(), download.Body.Bytes()) {
			t.Fatalf("preview did not serve the original version %s", source.material.MaterialID)
		}
	}
	request(otherID, first, "/image-preview", http.StatusNotFound)
	request(projectID, invalid, "/image-preview", http.StatusUnprocessableEntity)
	request(projectID, animated, "/image-preview", http.StatusUnprocessableEntity)
}
