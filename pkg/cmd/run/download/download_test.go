package download

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/cli/cli/v2/internal/ghrepo"
	"github.com/cli/cli/v2/internal/prompter"
	"github.com/cli/cli/v2/pkg/cmd/run/shared"
	"github.com/cli/cli/v2/pkg/cmdutil"
	"github.com/cli/cli/v2/pkg/iostreams"
	"github.com/google/shlex"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

func Test_NewCmdDownload(t *testing.T) {
	tests := []struct {
		name    string
		args    string
		isTTY   bool
		want    DownloadOptions
		wantErr string
	}{
		{
			name:  "empty",
			args:  "",
			isTTY: true,
			want: DownloadOptions{
				RunID:          "",
				DoPrompt:       true,
				Names:          []string(nil),
				DestinationDir: ".",
			},
		},
		{
			name:  "with run ID",
			args:  "2345",
			isTTY: true,
			want: DownloadOptions{
				RunID:          "2345",
				DoPrompt:       false,
				Names:          []string(nil),
				DestinationDir: ".",
			},
		},
		{
			name:  "to destination",
			args:  "2345 -D tmp/dest",
			isTTY: true,
			want: DownloadOptions{
				RunID:          "2345",
				DoPrompt:       false,
				Names:          []string(nil),
				DestinationDir: "tmp/dest",
			},
		},
		{
			name:  "repo level with names",
			args:  "-n one -n two",
			isTTY: true,
			want: DownloadOptions{
				RunID:          "",
				DoPrompt:       false,
				Names:          []string{"one", "two"},
				DestinationDir: ".",
			},
		},
		{
			name:  "repo level with patterns",
			args:  "-p o*e -p tw*",
			isTTY: true,
			want: DownloadOptions{
				RunID:          "",
				DoPrompt:       false,
				FilePatterns:   []string{"o*e", "tw*"},
				DestinationDir: ".",
			},
		},
		{
			name:  "repo level with names and patterns",
			args:  "-p o*e -p tw* -n three -n four",
			isTTY: true,
			want: DownloadOptions{
				RunID:          "",
				DoPrompt:       false,
				Names:          []string{"three", "four"},
				FilePatterns:   []string{"o*e", "tw*"},
				DestinationDir: ".",
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ios, _, _, _ := iostreams.Test()
			ios.SetStdoutTTY(tt.isTTY)
			ios.SetStdinTTY(tt.isTTY)
			ios.SetStderrTTY(tt.isTTY)

			f := &cmdutil.Factory{
				IOStreams: ios,
				HttpClient: func() (*http.Client, error) {
					return nil, nil
				},
				BaseRepo: func() (ghrepo.Interface, error) {
					return nil, nil
				},
			}

			var opts *DownloadOptions
			cmd := NewCmdDownload(f, func(o *DownloadOptions) error {
				opts = o
				return nil
			})
			cmd.PersistentFlags().StringP("repo", "R", "", "")

			argv, err := shlex.Split(tt.args)
			require.NoError(t, err)
			cmd.SetArgs(argv)

			cmd.SetIn(&bytes.Buffer{})
			cmd.SetOut(io.Discard)
			cmd.SetErr(io.Discard)

			_, err = cmd.ExecuteC()
			if tt.wantErr != "" {
				require.EqualError(t, err, tt.wantErr)
				return
			} else {
				require.NoError(t, err)
			}

			assert.Equal(t, tt.want.RunID, opts.RunID)
			assert.Equal(t, tt.want.Names, opts.Names)
			assert.Equal(t, tt.want.FilePatterns, opts.FilePatterns)
			assert.Equal(t, tt.want.DestinationDir, opts.DestinationDir)
			assert.Equal(t, tt.want.DoPrompt, opts.DoPrompt)
		})
	}
}

type testArtifact struct {
	artifact shared.Artifact
	files    []string
}

type fakePlatform struct {
	runArtifacts map[string][]testArtifact
}

func (f *fakePlatform) List(runID string) ([]shared.Artifact, error) {
	var runIds []string
	if runID != "" {
		runIds = []string{runID}
	} else {
		for k := range f.runArtifacts {
			runIds = append(runIds, k)
		}
	}

	var artifacts []shared.Artifact
	for _, id := range runIds {
		for _, a := range f.runArtifacts[id] {
			artifacts = append(artifacts, a.artifact)
		}
	}

	return artifacts, nil
}

func (f *fakePlatform) Download(url string, dir string) error {
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	// Now to be consistent, we find the artifact with the provided URL.
	// It's a bit janky to iterate the runs, to find the right artifact
	// rather than keying directly to it, but it allows the setup of the
	// fake platform to be declarative rather than imperative.
	// Think fakePlatform { artifacts: ... } rather than fakePlatform.makeArtifactAvailable()
	for _, testArtifacts := range f.runArtifacts {
		for _, testArtifact := range testArtifacts {
			if testArtifact.artifact.DownloadURL == url {
				for _, file := range testArtifact.files {
					path := filepath.Join(dir, file)
					return os.WriteFile(path, []byte{}, 0600)
				}
			}
		}
	}

	return errors.New("no artifact matches the provided URL")
}

func Test_runDownloadFake(t *testing.T) {
	tests := []struct {
		name          string
		opts          DownloadOptions
		platform      *fakePlatform
		promptStubs   func(*prompter.MockPrompter)
		expectedFiles []string
		wantErr       string
	}{
		{
			name: "download non-expired",
			opts: DownloadOptions{
				RunID: "2345",
			},
			platform: &fakePlatform{
				runArtifacts: map[string][]testArtifact{
					"2345": {
						{
							artifact: shared.Artifact{
								Name:        "artifact-1",
								DownloadURL: "http://download.com/artifact1.zip",
								Expired:     false,
							},
							files: []string{
								"artifact-1",
							},
						},
						{
							artifact: shared.Artifact{
								Name:        "expired-artifact",
								DownloadURL: "http://download.com/expired.zip",
								Expired:     true,
							},
							files: []string{
								"expired",
							},
						},
						{
							artifact: shared.Artifact{
								Name:        "artifact-2",
								DownloadURL: "http://download.com/artifact2.zip",
								Expired:     false,
							},
							files: []string{
								"artifact-2",
							},
						},
					},
				},
			},
			expectedFiles: []string{
				filepath.Join("artifact-1", "artifact-1"),
				filepath.Join("artifact-2", "artifact-2"),
			},
		},
		{
			name: "all artifacts are expired",
			opts: DownloadOptions{
				RunID: "2345",
			},
			platform: &fakePlatform{
				runArtifacts: map[string][]testArtifact{
					"2345": {
						{
							artifact: shared.Artifact{
								Name:        "artifact-1",
								DownloadURL: "http://download.com/artifact1.zip",
								Expired:     true,
							},
							files: []string{
								"artifact-1",
							},
						},
						{
							artifact: shared.Artifact{
								Name:        "artifact-2",
								DownloadURL: "http://download.com/artifact2.zip",
								Expired:     true,
							},
							files: []string{
								"artifact-2",
							},
						},
					},
				},
			},
			expectedFiles: []string{},
			wantErr:       "no valid artifacts found to download",
		},
		{
			name: "no name matches",
			opts: DownloadOptions{
				RunID: "2345",
				Names: []string{"artifact-3"},
			},
			platform: &fakePlatform{
				runArtifacts: map[string][]testArtifact{
					"2345": {
						{
							artifact: shared.Artifact{
								Name:        "artifact-1",
								DownloadURL: "http://download.com/artifact1.zip",
								Expired:     false,
							},
							files: []string{
								"artifact-1",
							},
						},
						{
							artifact: shared.Artifact{
								Name:        "artifact-2",
								DownloadURL: "http://download.com/artifact2.zip",
								Expired:     false,
							},
							files: []string{
								"artifact-2",
							},
						},
					},
				},
			},
			expectedFiles: []string{},
			wantErr:       "no artifact matches any of the names or patterns provided",
		},
		{
			name: "no pattern matches",
			opts: DownloadOptions{
				RunID:        "2345",
				FilePatterns: []string{"artifiction-*"},
			},
			platform: &fakePlatform{
				runArtifacts: map[string][]testArtifact{
					"2345": {
						{
							artifact: shared.Artifact{
								Name:        "artifact-1",
								DownloadURL: "http://download.com/artifact1.zip",
								Expired:     false,
							},
							files: []string{
								"artifact-1",
							},
						},
						{
							artifact: shared.Artifact{
								Name:        "artifact-2",
								DownloadURL: "http://download.com/artifact2.zip",
								Expired:     false,
							},
							files: []string{
								"artifact-2",
							},
						},
					},
				},
			},
			expectedFiles: []string{},
			wantErr:       "no artifact matches any of the names or patterns provided",
		},
		{
			name: "prompt to select artifact",
			opts: DownloadOptions{
				RunID:    "",
				DoPrompt: true,
				Names:    []string(nil),
			},
			platform: &fakePlatform{
				runArtifacts: map[string][]testArtifact{
					"2345": {
						{
							artifact: shared.Artifact{
								Name:        "artifact-1",
								DownloadURL: "http://download.com/artifact1.zip",
								Expired:     false,
							},
							files: []string{
								"artifact-1",
							},
						},
						{
							artifact: shared.Artifact{
								Name:        "expired-artifact",
								DownloadURL: "http://download.com/expired.zip",
								Expired:     true,
							},
							files: []string{
								"expired",
							},
						},
					},
					"6789": {
						{
							artifact: shared.Artifact{
								Name:        "artifact-2",
								DownloadURL: "http://download.com/artifact2.zip",
								Expired:     false,
							},
							files: []string{
								"artifact-2",
							},
						},
					},
				},
			},
			promptStubs: func(pm *prompter.MockPrompter) {
				pm.RegisterMultiSelect("Select artifacts to download:", nil, []string{"artifact-1", "artifact-2"},
					func(_ string, _, opts []string) ([]int, error) {
						for i, o := range opts {
							if o == "artifact-2" {
								return []int{i}, nil
							}
						}
						return nil, fmt.Errorf("no artifact-2 found in %v", opts)
					})
			},
			expectedFiles: []string{
				filepath.Join("artifact-2"),
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			opts := &tt.opts
			opts.DestinationDir = t.TempDir()
			ios, _, stdout, stderr := iostreams.Test()
			opts.IO = ios
			opts.Platform = tt.platform

			pm := prompter.NewMockPrompter(t)
			opts.Prompter = pm
			if tt.promptStubs != nil {
				tt.promptStubs(pm)
			}

			err := runDownload(opts)
			if tt.wantErr != "" {
				require.EqualError(t, err, tt.wantErr)
			} else {
				require.NoError(t, err)
			}

			for _, name := range tt.expectedFiles {
				require.FileExists(t, filepath.Join(opts.DestinationDir, name))
			}

			assert.Equal(t, "", stdout.String())
			assert.Equal(t, "", stderr.String())
		})
	}
}

func Test_runDownload(t *testing.T) {
	tests := []struct {
		name        string
		opts        DownloadOptions
		mockAPI     func(*mockPlatform)
		promptStubs func(*prompter.MockPrompter)
		wantErr     string
	}{
		{
			name: "download non-expired",
			opts: DownloadOptions{
				RunID:          "2345",
				DestinationDir: "./tmp",
				Names:          []string(nil),
			},
			mockAPI: func(p *mockPlatform) {
				p.On("List", "2345").Return([]shared.Artifact{
					{
						Name:        "artifact-1",
						DownloadURL: "http://download.com/artifact1.zip",
						Expired:     false,
					},
					{
						Name:        "expired-artifact",
						DownloadURL: "http://download.com/expired.zip",
						Expired:     true,
					},
					{
						Name:        "artifact-2",
						DownloadURL: "http://download.com/artifact2.zip",
						Expired:     false,
					},
				}, nil)
				p.On("Download", "http://download.com/artifact1.zip", filepath.FromSlash("tmp/artifact-1")).Return(nil)
				p.On("Download", "http://download.com/artifact2.zip", filepath.FromSlash("tmp/artifact-2")).Return(nil)
			},
		},
		{
			name: "no valid artifacts",
			opts: DownloadOptions{
				RunID:          "2345",
				DestinationDir: ".",
				Names:          []string(nil),
			},
			mockAPI: func(p *mockPlatform) {
				p.On("List", "2345").Return([]shared.Artifact{
					{
						Name:        "artifact-1",
						DownloadURL: "http://download.com/artifact1.zip",
						Expired:     true,
					},
					{
						Name:        "artifact-2",
						DownloadURL: "http://download.com/artifact2.zip",
						Expired:     true,
					},
				}, nil)
			},
			wantErr: "no valid artifacts found to download",
		},
		{
			name: "no name matches",
			opts: DownloadOptions{
				RunID:          "2345",
				DestinationDir: ".",
				Names:          []string{"artifact-3"},
			},
			mockAPI: func(p *mockPlatform) {
				p.On("List", "2345").Return([]shared.Artifact{
					{
						Name:        "artifact-1",
						DownloadURL: "http://download.com/artifact1.zip",
						Expired:     false,
					},
					{
						Name:        "artifact-2",
						DownloadURL: "http://download.com/artifact2.zip",
						Expired:     false,
					},
				}, nil)
			},
			wantErr: "no artifact matches any of the names or patterns provided",
		},
		{
			name: "no pattern matches",
			opts: DownloadOptions{
				RunID:          "2345",
				DestinationDir: ".",
				FilePatterns:   []string{"artifiction-*"},
			},
			mockAPI: func(p *mockPlatform) {
				p.On("List", "2345").Return([]shared.Artifact{
					{
						Name:        "artifact-1",
						DownloadURL: "http://download.com/artifact1.zip",
						Expired:     false,
					},
					{
						Name:        "artifact-2",
						DownloadURL: "http://download.com/artifact2.zip",
						Expired:     false,
					},
				}, nil)
			},
			wantErr: "no artifact matches any of the names or patterns provided",
		},
		{
			name: "prompt to select artifact",
			opts: DownloadOptions{
				RunID:          "",
				DoPrompt:       true,
				DestinationDir: ".",
				Names:          []string(nil),
			},
			mockAPI: func(p *mockPlatform) {
				p.On("List", "").Return([]shared.Artifact{
					{
						Name:        "artifact-1",
						DownloadURL: "http://download.com/artifact1.zip",
						Expired:     false,
					},
					{
						Name:        "expired-artifact",
						DownloadURL: "http://download.com/expired.zip",
						Expired:     true,
					},
					{
						Name:        "artifact-2",
						DownloadURL: "http://download.com/artifact2.zip",
						Expired:     false,
					},
					{
						Name:        "artifact-2",
						DownloadURL: "http://download.com/artifact2.also.zip",
						Expired:     false,
					},
				}, nil)
				p.On("Download", "http://download.com/artifact2.zip", ".").Return(nil)
			},
			promptStubs: func(pm *prompter.MockPrompter) {
				pm.RegisterMultiSelect("Select artifacts to download:", nil, []string{"artifact-1", "artifact-2"},
					func(_ string, _, opts []string) ([]int, error) {
						return []int{1}, nil
					})
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			opts := &tt.opts
			ios, _, stdout, stderr := iostreams.Test()
			opts.IO = ios
			opts.Platform = newMockPlatform(t, tt.mockAPI)

			pm := prompter.NewMockPrompter(t)
			opts.Prompter = pm
			if tt.promptStubs != nil {
				tt.promptStubs(pm)
			}

			err := runDownload(opts)
			if tt.wantErr != "" {
				require.EqualError(t, err, tt.wantErr)
			} else {
				require.NoError(t, err)
			}

			assert.Equal(t, "", stdout.String())
			assert.Equal(t, "", stderr.String())
		})
	}
}

type mockPlatform struct {
	mock.Mock
}

func newMockPlatform(t *testing.T, config func(*mockPlatform)) *mockPlatform {
	m := &mockPlatform{}
	m.Test(t)
	t.Cleanup(func() {
		m.AssertExpectations(t)
	})
	if config != nil {
		config(m)
	}
	return m
}

func (p *mockPlatform) List(runID string) ([]shared.Artifact, error) {
	args := p.Called(runID)
	return args.Get(0).([]shared.Artifact), args.Error(1)
}

func (p *mockPlatform) Download(url string, dir string) error {
	args := p.Called(url, dir)
	return args.Error(0)
}
