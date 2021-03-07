package cmd

import (
	"context"
	"fmt"
	"os"
	"path"
	"reflect"
	"testing"

	"github.com/release-engineering/exodus-rsync/internal/conf"
	"github.com/release-engineering/exodus-rsync/internal/gw"
	"github.com/release-engineering/exodus-rsync/internal/walk"
)

const CONFIG string = `
environments:
- prefix: exodus
  gwenv: best-env
`

type EnvMatcher struct {
	name string
}

func (m EnvMatcher) Matches(x interface{}) bool {
	env, ok := x.(conf.Environment)
	if !ok {
		return false
	}
	return env.GwEnv == m.name
}

func (m EnvMatcher) String() string {
	return fmt.Sprintf("Environment '%s'", m.name)
}

type FakeClient struct {
	blobs     map[string]string
	publishes []FakePublish
}

type FakePublish struct {
	items     []gw.ItemInput
	committed int
	id        string
}

func (c *FakeClient) EnsureUploaded(ctx context.Context, items []walk.SyncItem,
	onUploaded func(walk.SyncItem) error,
	onExisting func(walk.SyncItem) error,
) error {
	var err error

	for _, item := range items {
		if _, ok := c.blobs[item.Key]; ok {
			err = onExisting(item)
		} else {
			c.blobs[item.Key] = item.SrcPath
			err = onUploaded(item)
		}
		if err != nil {
			return err
		}
	}

	return nil
}

func (c *FakeClient) NewPublish(ctx context.Context) (gw.Publish, error) {
	c.publishes = append(c.publishes, FakePublish{id: "some-publish"})
	return &c.publishes[len(c.publishes)-1], nil
}

func (p *FakePublish) AddItems(ctx context.Context, items []gw.ItemInput) error {
	if p.committed != 0 {
		return fmt.Errorf("attempted to modify committed publish")
	}
	p.items = append(p.items, items...)
	return nil
}

func (p *FakePublish) Commit(ctx context.Context) error {
	p.committed++
	return nil
}

func (p *FakePublish) ID() string {
	return p.id
}

func TestMainTypicalSync(t *testing.T) {
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}

	SetConfig(t, CONFIG)
	ctrl := MockController(t)

	mockGw := gw.NewMockInterface(ctrl)
	ext.gw = mockGw

	client := FakeClient{blobs: make(map[string]string)}
	mockGw.EXPECT().NewClient(EnvMatcher{"best-env"}).Return(&client, nil)

	srcPath := path.Clean(wd + "/../../test/data/srctrees/just-files")

	args := []string{
		"rsync",
		srcPath,
		"exodus:/some/target",
	}

	got := Main(args)

	// It should complete successfully.
	if got != 0 {
		t.Error("returned incorrect exit code", got)
	}

	// Check paths of some blobs we expected to deal with.
	binPath := client.blobs["c66f610d98b2c9fe0175a3e99ba64d7fc7de45046515ff325be56329a9347dd6"]
	helloPath := client.blobs["5891b5b522d5df086d0ff0b110fbd9d21bb4fc7163af34d08286a2e846f6be03"]

	// It should have uploaded the binary from here
	if binPath != srcPath+"/subdir/some-binary" {
		t.Error("binary uploaded from unexpected path", binPath)
	}

	// For the hello file, since there were two copies, it's undefined which one of them
	// was used for the upload - but should be one of them.
	if helloPath != srcPath+"/hello-copy-one" && helloPath != srcPath+"/hello-copy-two" {
		t.Error("hello uploaded from unexpected path", helloPath)
	}

	// It should have created one publish.
	if len(client.publishes) != 1 {
		t.Error("expected to create 1 publish, instead created", len(client.publishes))
	}

	p := client.publishes[0]

	// Build up a URI => Key mapping of what was published
	itemMap := make(map[string]string)
	for _, item := range p.items {
		if _, ok := itemMap[item.WebURI]; ok {
			t.Error("tried to publish this URI more than once:", item.WebURI)
		}
		itemMap[item.WebURI] = item.ObjectKey
	}

	// It should have been exactly this
	expectedItems := map[string]string{
		"/some/target/subdir/some-binary": "c66f610d98b2c9fe0175a3e99ba64d7fc7de45046515ff325be56329a9347dd6",
		"/some/target/hello-copy-one":     "5891b5b522d5df086d0ff0b110fbd9d21bb4fc7163af34d08286a2e846f6be03",
		"/some/target/hello-copy-two":     "5891b5b522d5df086d0ff0b110fbd9d21bb4fc7163af34d08286a2e846f6be03",
	}

	if !reflect.DeepEqual(itemMap, expectedItems) {
		t.Error("did not publish expected items, published:", itemMap)
	}

	// It should have committed the publish (once)
	if p.committed != 1 {
		t.Error("expected to commit publish (once), instead p.committed ==", p.committed)
	}
}

func TestMainSyncFollowsLinks(t *testing.T) {
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}

	SetConfig(t, CONFIG)
	ctrl := MockController(t)

	mockGw := gw.NewMockInterface(ctrl)
	ext.gw = mockGw

	client := FakeClient{blobs: make(map[string]string)}
	mockGw.EXPECT().NewClient(EnvMatcher{"best-env"}).Return(&client, nil)

	srcPath := path.Clean(wd + "/../../test/data/srctrees/links")

	args := []string{
		"rsync",
		"-vvv",
		srcPath,
		"exodus:/dest",
	}

	got := Main(args)

	// It should complete successfully.
	if got != 0 {
		t.Error("returned incorrect exit code", got)
	}

	// It should have created one publish.
	if len(client.publishes) != 1 {
		t.Error("expected to create 1 publish, instead created", len(client.publishes))
	}

	p := client.publishes[0]

	// Build up a URI => Key mapping of what was published
	itemMap := make(map[string]string)
	for _, item := range p.items {
		if _, ok := itemMap[item.WebURI]; ok {
			t.Error("tried to publish this URI more than once:", item.WebURI)
		}
		itemMap[item.WebURI] = item.ObjectKey
	}

	// It should have been exactly this
	expectedItems := map[string]string{
		"/dest/link-to-regular-file":          "5891b5b522d5df086d0ff0b110fbd9d21bb4fc7163af34d08286a2e846f6be03",
		"/dest/subdir/regular-file":           "5891b5b522d5df086d0ff0b110fbd9d21bb4fc7163af34d08286a2e846f6be03",
		"/dest/subdir/rand1":                  "57921e8a0929eaff5003cc9dd528c3421296055a4de2ba72429dc7f41bfa8411",
		"/dest/subdir/rand2":                  "f3a5340ae2a400803b8150f455ad285d173cbdcf62c8e9a214b30f467f45b310",
		"/dest/subdir2/dir-link/regular-file": "5891b5b522d5df086d0ff0b110fbd9d21bb4fc7163af34d08286a2e846f6be03",
		"/dest/subdir2/dir-link/rand1":        "57921e8a0929eaff5003cc9dd528c3421296055a4de2ba72429dc7f41bfa8411",
		"/dest/subdir2/dir-link/rand2":        "f3a5340ae2a400803b8150f455ad285d173cbdcf62c8e9a214b30f467f45b310",
	}

	if !reflect.DeepEqual(itemMap, expectedItems) {
		t.Error("did not publish expected items, published:", itemMap)
	}

	// It should have committed the publish (once)
	if p.committed != 1 {
		t.Error("expected to commit publish (once), instead p.committed ==", p.committed)
	}
}
