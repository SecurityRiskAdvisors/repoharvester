package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"github.com/jessevdk/go-flags"
	"golang.org/x/sync/semaphore"
	"io"
	"io/ioutil"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"text/tabwriter"
	"time"
)

type Repo struct {
	Name       string
	Clone_url  string
	Size       uint64
	Fork       bool
	local_path string // This will not be used by json to decode
}

type EmailContext struct {
	Repo         *Repo
	EmailAddress string
	Role         int8
}

const (
	ROLE_AUTHOR         int8   = 1 << iota
	ROLE_COMMITTER      int8   = 1 << iota
	ROLE_NAME_COMMITTER string = "Committer"
	ROLE_NAME_AUTHOR    string = "Author"
	ROLE_NAME_BOTH      string = "Author+Committer"
	ROLE_MASK_BOTH      int8   = ROLE_AUTHOR | ROLE_COMMITTER
)

type EmailGroupByRepoKey struct {
	Email string
	Repo  *Repo
}

type FmtEmailPerRepo struct {
	RepoUrl string
	Emails  map[string]string
}

type FmtRepoPerEmail struct {
	RepoName string
	Role     string
	RepoUrl  string
}

var active_data []uint32
var error_data []uint32
var completion_data []uint32
var total_data []uint32

const (
	GITHUB_FETCH       int8 = 0
	GITHUB_TOTAL_PAGES int8 = 0
	GITHUB_PARSE       int8 = 1
	REMOTE_REPOS       int8 = 1
	GIT_OPS_CLONE      int8 = 2
	LOCAL_REPOS        int8 = 2
	GIT_OPS_LOG        int8 = 3
	GIT_IDENTITIES     int8 = 3
	EMAILS_DEDUP       int8 = 4
	EMAILS_GROUPED     int8 = 5
)

const DEFAULT_SIZE_FILTER int = 1000000

var (
	LINE_SEP    string
	g_buff_pool sync.Pool
	g_semaphore *semaphore.Weighted
	BUFFER_SIZE int
)

// Logging code
const (
	LOG_FATAL uint8 = 0
	LOG_ERROR uint8 = 1
	LOG_INFO  uint8 = 2
	LOG_DEBUG uint8 = 3
)

type Logger struct {
	Panic     func(...interface{})
	Fatal     func(...interface{})
	Error     func(...interface{})
	Errorf    func(string, ...interface{})
	ErrorFunc func(func() (string, bool))
	Info      func(...interface{})
	Infof     func(string, ...interface{})
	InfoFunc  func(func() (string, bool))
	Debug     func(...interface{})
	Debugf    func(string, ...interface{})
	DebugFunc func(func() (string, bool))
	wg        sync.WaitGroup
	log_level uint8
}

func (logger *Logger) set_level(level uint8) bool {

	if level > 3 {
		return false
	}

	logger.log_level = level

	logger.Fatal = noop
	logger.Error = noop
	logger.Errorf = noopf
	logger.ErrorFunc = noopfunc
	logger.Info = noop
	logger.Infof = noopf
	logger.InfoFunc = noopfunc
	logger.Debug = noop
	logger.Debugf = noopf
	logger.DebugFunc = noopfunc

	switch level {
	case LOG_DEBUG:
		logger.Debug = logger.log_debug
		logger.Debugf = logger.logf_debug
		logger.DebugFunc = logger.logfunc_debug
		fallthrough
	case LOG_INFO:
		logger.Info = logger.log_info
		logger.Infof = logger.logf_info
		logger.InfoFunc = logger.logfunc_info
		fallthrough
	case LOG_ERROR:
		logger.Error = logger.log_error
		logger.Errorf = logger.logf_error
		logger.ErrorFunc = logger.logfunc_error
		fallthrough
	case LOG_FATAL:
		logger.Fatal = logger.log_fatal
		logger.Panic = logger.log_panic
	}

	return true

}

func (logger *Logger) LogLevel() uint8 {
	return logger.log_level
}

// Do the main logging functions in a goroutine to ensure they don't slow down the main operation
func (logger *Logger) log_debug(a ...interface{}) {
	logger.wg.Add(1)
	go func() {
		msg := fmt.Sprint(a...)
		log("DEBUG", &msg)
		logger.wg.Done()
	}()
}

func (logger *Logger) logf_debug(format string, a ...interface{}) {
	logger.wg.Add(1)
	go func() {
		msg := fmt.Sprintf(format, a...)
		log("DEBUG", &msg)
		logger.wg.Done()
	}()
}

func (logger *Logger) logfunc_debug(logfunc func() (string, bool)) {
	logger.wg.Add(1)
	go func() {
		msg, ok := logfunc()
		if ok {
			log("DEBUG", &msg)
		}
		logger.wg.Done()
	}()
}

func (logger *Logger) log_info(a ...interface{}) {
	logger.wg.Add(1)
	go func() {
		msg := fmt.Sprint(a...)
		log("INFO", &msg)
		logger.wg.Done()
	}()
}

func (logger *Logger) logf_info(format string, a ...interface{}) {
	logger.wg.Add(1)
	go func() {
		msg := fmt.Sprintf(format, a...)
		log("INFO", &msg)
		logger.wg.Done()
	}()
}

func (logger *Logger) logfunc_info(logfunc func() (string, bool)) {
	logger.wg.Add(1)
	go func() {
		msg, ok := logfunc()
		if ok {
			log("INFO", &msg)
		}
		logger.wg.Done()
	}()
}

func (logger *Logger) log_error(a ...interface{}) {
	logger.wg.Add(1)
	go func() {
		msg := fmt.Sprint(a...)
		log("ERROR", &msg)
		logger.wg.Done()
	}()
}
func (logger *Logger) logf_error(format string, a ...interface{}) {
	logger.wg.Add(1)
	go func() {
		msg := fmt.Sprintf(format, a...)
		log("ERROR", &msg)
		logger.wg.Done()
	}()
}

func (logger *Logger) logfunc_error(logfunc func() (string, bool)) {
	logger.wg.Add(1)
	go func() {
		msg, ok := logfunc()
		if ok {
			log("ERROR", &msg)
		}
		logger.wg.Done()
	}()
}

// These two functions terminate execution so they don't run async
func (logger *Logger) log_fatal(a ...interface{}) {
	msg := fmt.Sprint(a...)
	log("FATAL", &msg)
	os.Exit(1)
}
func (logger *Logger) log_panic(a ...interface{}) {
	msg := fmt.Sprint(a...)
	log("PANIC", &msg)
	panic(msg)
}

func log(level string, msg *string) {
	out := g_buff_pool.Get().(*bytes.Buffer)
	out.Reset()
	out.WriteString(level)
	out.WriteString(": ")
	out.WriteString(*msg)
	out.WriteString(LINE_SEP)
	out.WriteTo(os.Stderr)
	//fmt.Fprintf(os.Stderr, "%s: %s%s", level, *msg, LINE_SEP)
	g_buff_pool.Put(out)
}

func noop(a ...interface{}) {
	return
}

func noopf(format string, a ...interface{}) {
	return
}

func noopfunc(logfunc func() (string, bool)) {
	return
}

// Wait for any writers to return
// Kinda like a Sync()
func (logger *Logger) Wait() {
	logger.wg.Wait()
}

var logger Logger

// End logging functions

type ResourceOptions struct {
	Type       string `short:"t" long:"type" description:"type of object to target" choice:"user" choice:"org" choice:"url"`
	Org        bool   `short:"o" long:"org" description:"alias to --type org" group:"parse-type"`
	User       bool   `short:"u" long:"user" description:"alias to --type user" group:"parse-type"`
	Url        bool   `long:"url" description:"alias to --type url" group:"parse-type"`
	SizeFilter uint64 `long:"size-filter" value-name:"<size in kB>" description:"repo size to filter (set 0 to disable)" default:"1000000" long-description:"There are often repos that are asset heavy and increase the faceprint time without a lot of gain. This filters those out."`
	ForkFilter bool   `long:"no-fork" description:"filter out forked repos"`
}

type OutputOptions struct {
	OutputJson flags.Filename `short:"j" long:"json" description:"Output JSON file" value-name:"output.json" required:"true"`
	OutputFile flags.Filename `short:"f" long:"file" description:"Output flat file" value-name:"output.list" required:"true"`
}

type Positional struct {
	TargetName string `positional-arg-name:"target-name" description:"The name of the user or org to faceprint"`
}

type ApplicationOptions struct {
	Verbose     bool           `short:"v" long:"verbose" description:"Show verbose debug information"`
	Quiet       bool           `short:"q" long:"quiet" description:"Show fewer messages"`
	PreserveDir bool           `long:"preserve-dir" description:"preserve working directory"`
	WorkingDir  flags.Filename `short:"w" long:"working-dir" value-name:"<path_to_working_dir>" default:"!None-Provided!" default-mask:"Uses working directory" description:"working dir path (should have space to store all repos)"`
	GitPath     flags.Filename `long:"git-path" short:"g" description:"path to git" value-name:"<path_to_git>" default:"!None-Provided!" default-mask:"Uses system git"`
}

type AdvancedOptions struct {
	Workers   int8 `long:"workers" description:"numbers of workers to use" default:"20" value-name:"<int>"`
	QueueSize int  `long:"queue-size" description:"base size of the operating queue" default:"20" value-name:"<int>"`
}

var opts struct {
	//Name string `name:"name" description:"The name of the user or org to faceprint" positional-args:"yes" required:"yes"`
	Args        Positional         `positional-args:"yes" required:"yes"`
	Resource    ResourceOptions    `group:"Resource Options (Required)"`
	Output      OutputOptions      `group:"Output Options (Required)"`
	Application ApplicationOptions `group:"Application Options"`
	Advanced    AdvancedOptions    `group:"Advanced Options"`
}

var parser = flags.NewParser(&opts, flags.Default)

func check_working_dir(working_dir string) (bool, error) {

	err := os.MkdirAll(working_dir, 0700)
	if err != nil {
		return false, err
	}

	f, err := os.Open(working_dir)
	if err != nil {
		return false, err
	}
	defer f.Close()

	_, err = f.Readdir(1)
	if err != nil {
		if err == io.EOF {
			return true, nil
		}
		return false, err
	}
	return false, nil
}

func check_ouput_location(file string) (bool, error) {

	_, err := os.OpenFile(file, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0600)

	if err != nil {
		return false, err
	}

	return true, nil
}

func get_next_link(http_header map[string][]string, next_url *string) bool {
	val, ok := http_header["Link"]
	if ok {
		link_header := strings.Split(val[0], ", ")
		for _, value := range link_header {
			if strings.HasSuffix(value, `"next"`) {
				*next_url = strings.Split(value, "; ")[0]
				*next_url = (*next_url)[1 : len(*next_url)-1]
				return true
			}
		}
	}
	return false
}

func get_total_pages(http_header map[string][]string, total_pages *uint32) {
	// if we already set the total_pages -- we don't need to do it again
	if *total_pages > 0 {
		return
	}
	val, ok := http_header["Link"]
	if ok {
		link_header := strings.Split(val[0], ", ")
		for _, value := range link_header {
			if strings.HasSuffix(value, `"last"`) {
				url := strings.Split(value, "; ")[0]
				pages, err := strconv.ParseUint(url[len(url)-2:len(url)-1], 10, 32)
				if err != nil {
					panic(err)
				}
				*total_pages = uint32(pages)
				return
			}
		}
	}

	// If there is no Link header, then we only have one page
	*total_pages = 1
	return
}

func get_repos_from_github(ctx context.Context, url string) chan io.ReadCloser {

	func_logging_name := "Stage 1 - Get Github Repos"
	bodies := make(chan io.ReadCloser, BUFFER_SIZE)
	c := &http.Client{}
	urls := make(chan string, BUFFER_SIZE)
	urls <- url
	go func() {
		defer close(urls)
		defer close(bodies)

		err := g_semaphore.Acquire(ctx, 1)
		// If we get an error back, it means the context is done
		if err != nil {
			return
		}
		defer g_semaphore.Release(1)
		infoLogger := func() (string, bool) {
			active := atomic.LoadUint32(&active_data[GITHUB_FETCH])
			completed := atomic.LoadUint32(&completion_data[GITHUB_FETCH])
			total := atomic.LoadUint32(&total_data[GITHUB_TOTAL_PAGES])

			if active+completed+total > 0 {
				var out strings.Builder
				out.WriteString(func_logging_name)
				out.WriteString(": Processed ")
				out.WriteString(strconv.FormatUint(uint64(completed), 10))
				out.WriteString(" of a total ")
				out.WriteString(strconv.FormatUint(uint64(total), 10))
				out.WriteString(" pages. Active requests: ")
				out.WriteString(strconv.FormatUint(uint64(active), 10))
				out.WriteString(".")
				return out.String(), true
			}
			return "", false
		}

		var next_url string
		var total_pages uint32 = 0
		for {
			logger.DebugFunc(infoLogger)
			select {
			// select read from urls
			case <-ctx.Done():
				return
			case url := <-urls:
				atomic.AddUint32(&active_data[GITHUB_FETCH], 1)
				var fetch_counter int8 = 1

				//run the req with the context to cancel if needed
				req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
				if err != nil {
					logger.Error(func_logging_name, ": Error setting up request. Error: ", err)
					atomic.AddUint32(&error_data[GITHUB_FETCH], 1)
					atomic.AddUint32(&active_data[GITHUB_FETCH], ^uint32(0))
					return
				}
				for {
					resp, err := c.Do(req)
					if err != nil {
						if fetch_counter >= 4 {
							logger.Errorf("%s: Error fetching %s. Error:%v", func_logging_name, url, err)
							atomic.AddUint32(&error_data[GITHUB_FETCH], 1)
							atomic.AddUint32(&active_data[GITHUB_FETCH], ^uint32(0))
							return
						} else {
							logger.Debugf("%s: Attempt #%d. URL: %s. Error: %v", func_logging_name, fetch_counter, url, err)
							fetch_counter++
							continue
						}
					}
					get_total_pages(resp.Header, &total_pages)
					atomic.StoreUint32(&total_data[GITHUB_TOTAL_PAGES], total_pages)
					atomic.AddUint32(&completion_data[GITHUB_FETCH], 1)
					atomic.AddUint32(&active_data[GITHUB_FETCH], ^uint32(0))
					// select write to bodies
					select {
					case <-ctx.Done():
						return
					case bodies <- resp.Body:
					}

					ok := get_next_link(resp.Header, &next_url)
					if ok {
						// This should never block
						urls <- next_url
					} else {
						logger.Info(func_logging_name, ": Completed. Pages pulled: ", total_pages, ". Error count: ", atomic.LoadUint32(&error_data[GITHUB_FETCH]))
						return
					}
					break
				}
			}
		}
	}()
	return bodies
}

func parse_github_response(ctx context.Context, repo_data chan io.ReadCloser, fork_filter bool) chan Repo {
	func_logging_name := "Stage 2 - Parse URLs"
	repos := make(chan Repo, BUFFER_SIZE)
	go func() {
		var wg sync.WaitGroup
		infoLogger := func() (string, bool) {
			active := atomic.LoadUint32(&active_data[GITHUB_PARSE])
			completed := atomic.LoadUint32(&completion_data[GITHUB_PARSE])
			total := atomic.LoadUint32(&total_data[GITHUB_TOTAL_PAGES])

			if active+completed+total > 0 {
				var out strings.Builder
				out.WriteString(func_logging_name)
				out.WriteString(": Processed ")
				out.WriteString(strconv.FormatUint(uint64(completed), 10))
				out.WriteString(" of a total ")
				out.WriteString(strconv.FormatUint(uint64(total), 10))
				out.WriteString(" pages. Active pages: ")
				out.WriteString(strconv.FormatUint(uint64(active), 10))
				out.WriteString(".")
				return out.String(), true
			}
			return "", false
		}

		for {
			logger.DebugFunc(infoLogger)
			select {
			case <-ctx.Done():
				wg.Wait()
				close(repos)
				return
			case body, ok := <-repo_data:
				if !ok {
					logger.Debug(func_logging_name, ": cleared queue of size: ", atomic.LoadUint32(&total_data[GITHUB_TOTAL_PAGES]), " - in-flight actions: ", atomic.LoadUint32(&active_data[GITHUB_PARSE]))
					wg.Wait()
					close(repos)
					logger.Info(func_logging_name, ": Completed. Total Pages Parsed: ", atomic.LoadUint32(&completion_data[GITHUB_PARSE]), ". Work Items Created: ", atomic.LoadUint32(&total_data[REMOTE_REPOS]), ". Error count: ", atomic.LoadUint32(&error_data[GITHUB_PARSE]))
					return
				}
				err := g_semaphore.Acquire(ctx, 1)
				// If we get an error back, it means the context is done
				if err != nil {
					wg.Wait()
					close(repos)
					return
				}
				wg.Add(1)
				atomic.AddUint32(&active_data[GITHUB_PARSE], 1)
				go func(body io.ReadCloser) {
					defer wg.Done()
					defer g_semaphore.Release(1)
					dec := json.NewDecoder(body)
					for {
						var r []Repo
						if err := dec.Decode(&r); err == io.EOF {
							body.Close()
							break
						} else if err != nil {
							logger.Error(func_logging_name, ": Error parsing body. Error: ", err)
							atomic.AddUint32(&error_data[GITHUB_PARSE], 1)
							atomic.AddUint32(&active_data[GITHUB_PARSE], ^uint32(0))
							// Not sure if I should continue or just exit.
							// Exiting for now, can change it to continue
							return
						}
						for _, repo := range r {
							if repo.Fork && fork_filter {
								logger.Debug(func_logging_name, ": Skipping ", repo.Name, " based on the fork filter.")
								continue
							}
							select {
							case <-ctx.Done():
								return
							case repos <- repo:
								atomic.AddUint32(&total_data[REMOTE_REPOS], 1)
							}
						}
						atomic.AddUint32(&completion_data[GITHUB_PARSE], 1)
						atomic.AddUint32(&active_data[GITHUB_PARSE], ^uint32(0))
					}
				}(body)
			}
		}
		wg.Wait()
		close(repos)
	}()

	return repos
}

func git_ops_clone(ctx context.Context, repos chan Repo, git_path *string, working_dir *string, size_filter uint64) chan Repo {
	local_repos := make(chan Repo, BUFFER_SIZE)
	func_logging_name := "Stage 3 - Clone Repos"
	go func() {
		var wg sync.WaitGroup
		infoLogger := func() (string, bool) {
			active := atomic.LoadUint32(&active_data[GIT_OPS_CLONE])
			completed := atomic.LoadUint32(&completion_data[GIT_OPS_CLONE])
			total := atomic.LoadUint32(&total_data[REMOTE_REPOS])

			if active+completed+total > 0 && completed%3 == 0 {
				var out strings.Builder
				out.WriteString(func_logging_name)
				out.WriteString(": Cloned ")
				out.WriteString(strconv.FormatUint(uint64(completed), 10))
				out.WriteString(" of a total ")
				out.WriteString(strconv.FormatUint(uint64(total), 10))
				out.WriteString(" repos. Active clones: ")
				out.WriteString(strconv.FormatUint(uint64(active), 10))
				out.WriteString(".")
				return out.String(), true
			}
			return "", false
		}
		for {
			logger.DebugFunc(infoLogger)
			select {
			case <-ctx.Done():
				wg.Wait()
				close(local_repos)
				return
			case repo, ok := <-repos:
				if !ok {
					logger.Debug(func_logging_name, ": cleared queue of size: ", atomic.LoadUint32(&total_data[REMOTE_REPOS]), " - in-flight actions: ", atomic.LoadUint32(&active_data[GIT_OPS_CLONE]))
					wg.Wait()
					close(local_repos)
					logger.Info(func_logging_name, ": Completed. Total repos cloned: ", atomic.LoadUint32(&completion_data[GIT_OPS_CLONE]), ". Work Items Created: ", atomic.LoadUint32(&total_data[LOCAL_REPOS]), ". Error count: ", atomic.LoadUint32(&error_data[GIT_OPS_CLONE]))
					return
				}
				if size_filter > 0 && repo.Size > size_filter {
					atomic.AddUint32(&completion_data[GIT_OPS_CLONE], 1)
					logger.Infof("%s: Skipping %s of size %d based on filter %d.", func_logging_name, repo.Name, repo.Size, size_filter)
					continue
				}
				err := g_semaphore.Acquire(ctx, 1)
				if err != nil {
					wg.Wait()
					close(local_repos)
					return
				}
				wg.Add(1)
				atomic.AddUint32(&active_data[GIT_OPS_CLONE], 1)
				go func() {
					defer wg.Done()
					defer g_semaphore.Release(1)
					cmd := exec.CommandContext(ctx, *git_path, "clone", "-n", "-q", repo.Clone_url)
					cmd.Dir = *working_dir
					std_err := g_buff_pool.Get().(*bytes.Buffer)
					std_err.Reset()
					defer g_buff_pool.Put(std_err)
					cmd.Stderr = std_err
					err := cmd.Run()
					if err != nil {
						switch err_defined := err.(type) {
						case *exec.ExitError:
							// Probably a context kill
							if !err_defined.ProcessState.Exited() && err_defined.ProcessState.ExitCode() == -1 {
								// Really probably an ctx kill so we'll make this log level info
								logger.Debug(func_logging_name, ": ", repo.Name, " killed by application interrupt. Error: ", err, ". Error from application: ", std_err.String())
								repo.local_path = filepath.Join(*working_dir, repo.Name)
								atomic.AddUint32(&error_data[GIT_OPS_CLONE], 1)
								atomic.AddUint32(&active_data[GIT_OPS_CLONE], ^uint32(0))
								return
							}
							// otherwise, things are probably bad. This will be log level error
							logger.Error(func_logging_name, ": Got an error. Repo Name: ", repo.Name, " - golang err: ", err, ". Error from command: ", std_err.String())
							repo.local_path = filepath.Join(*working_dir, repo.Name)
							atomic.AddUint32(&error_data[GIT_OPS_CLONE], 1)
							atomic.AddUint32(&active_data[GIT_OPS_CLONE], ^uint32(0))
							return
						default:
							// All other cases are log level error
							logger.Error(func_logging_name, ": Got an error. Repo Name: ", repo.Name, " - golang err: ", err, ". Error from command: ", std_err.String())
							repo.local_path = filepath.Join(*working_dir, repo.Name)
							atomic.AddUint32(&error_data[GIT_OPS_CLONE], 1)
							atomic.AddUint32(&active_data[GIT_OPS_CLONE], ^uint32(0))
							return
						}
					}
					repo.local_path = filepath.Join(*working_dir, repo.Name)
					select {
					case <-ctx.Done():
						return
					case local_repos <- repo:
						atomic.AddUint32(&total_data[LOCAL_REPOS], 2)
						atomic.AddUint32(&completion_data[GIT_OPS_CLONE], 1)
						atomic.AddUint32(&active_data[GIT_OPS_CLONE], ^uint32(0))
						return
					}
				}()
			}
		}
	}()
	return local_repos
}

func git_ops_shortlog(ctx context.Context, local_repos chan Repo, git_path *string) (chan string, chan EmailContext) {
	emails := make(chan string, BUFFER_SIZE)
	context_emails := make(chan EmailContext, BUFFER_SIZE)
	func_logging_name := "Stage 4 - Find Emails"

	go func() {
		var wg sync.WaitGroup
		l_semaphore := semaphore.NewWeighted(2)
		var sem *semaphore.Weighted
		params_containers := map[int8][]string{ROLE_AUTHOR: []string{"--no-pager", "shortlog", "--all", "-n", "-e", "-s"}, ROLE_COMMITTER: []string{"--no-pager", "shortlog", "--all", "-n", "-e", "-s", "-c"}}
		infoLogger := func() (string, bool) {
			active := atomic.LoadUint32(&active_data[GIT_OPS_LOG])
			completed := atomic.LoadUint32(&completion_data[GIT_OPS_LOG])
			total := atomic.LoadUint32(&total_data[LOCAL_REPOS])

			if active+completed+total > 0 && completed%3 == 0 {
				var out strings.Builder
				out.WriteString(func_logging_name)
				out.WriteString(": Processsed ")
				out.WriteString(strconv.FormatUint(uint64(completed), 10))
				out.WriteString(" of a total ")
				out.WriteString(strconv.FormatUint(uint64(total), 10))
				out.WriteString(" repos. Active shortlogs: ")
				out.WriteString(strconv.FormatUint(uint64(active), 10))
				out.WriteString(".")
				return out.String(), true
			}
			return "", false
		}
		for {
			logger.DebugFunc(infoLogger)
			select {
			case <-ctx.Done():
				wg.Wait()
				close(emails)
				close(context_emails)
				return
			case repo, ok := <-local_repos:
				if !ok {
					logger.Debug(func_logging_name, ": completed queue of size: ", atomic.LoadUint32(&total_data[LOCAL_REPOS]), " - in-flight actions: ", atomic.LoadUint32(&active_data[GIT_OPS_LOG]))
					wg.Wait()
					close(emails)
					close(context_emails)
					logger.Info(func_logging_name, ": Completed. Total repos processed: ", atomic.LoadUint32(&completion_data[GIT_OPS_LOG]), ". Work Items Created: ", atomic.LoadUint32(&total_data[GIT_IDENTITIES]), ". Error count: ", atomic.LoadUint32(&error_data[GIT_OPS_LOG]))
					return
				}
				for role, params := range params_containers {
					if !l_semaphore.TryAcquire(1) {
						err := g_semaphore.Acquire(ctx, 1)
						if err != nil {
							wg.Wait()
							close(emails)
							close(context_emails)
							return
						}
						sem = g_semaphore
					} else {
						sem = l_semaphore
					}
					wg.Add(1)
					atomic.AddUint32(&active_data[GIT_OPS_LOG], 1)
					go func(params []string, role int8, sem *semaphore.Weighted) {
						defer wg.Done()
						defer sem.Release(1)
						//author_cmd := exec.CommandContext(ctx, *git_path, "--no-pager", "shortlog", "--all", "-n", "-e", "-s")
						//commiter_cmd := exec.CommandContext(ctx, *git_path, "shortlog", "--all", "-n", "-e", "-s", "-c")
						cmd := exec.CommandContext(ctx, *git_path, params...)
						cmd.Dir = repo.local_path
						std_out := g_buff_pool.Get().(*bytes.Buffer)
						std_out.Reset()
						defer g_buff_pool.Put(std_out)
						cmd.Stdout = std_out
						std_err := g_buff_pool.Get().(*bytes.Buffer)
						std_err.Reset()
						defer g_buff_pool.Put(std_err)
						cmd.Stderr = std_err

						err := cmd.Run()
						if err != nil {
							switch err_defined := err.(type) {
							case *exec.ExitError:
								// Probably a context kill
								if !err_defined.ProcessState.Exited() && err_defined.ProcessState.ExitCode() == -1 {
									// Really probably an ctx kill so we'll make this log level info
									logger.Debug(func_logging_name, ": ", repo.Name, " killed by interrupt. Error: ", err, ". Error from application: ", std_err.String())
									atomic.AddUint32(&error_data[GIT_OPS_LOG], 1)
									atomic.AddUint32(&active_data[GIT_OPS_LOG], ^uint32(0))
									return
								}
								// otherwise, things are probably bad. This will be log level error
								logger.Error(func_logging_name, ": Got an error. Repo Name: ", repo.Name, " - golang err: ", err, ". Error from command: ", std_err.String())
								atomic.AddUint32(&error_data[GIT_OPS_LOG], 1)
								atomic.AddUint32(&active_data[GIT_OPS_LOG], ^uint32(0))
								return
							default:
								// All other cases are log level error
								logger.Error(func_logging_name, ": Got an error. Repo Name: ", repo.Name, " - golang err: ", err, ". Error from command: ", std_err.String())
								atomic.AddUint32(&error_data[GIT_OPS_LOG], 1)
								atomic.AddUint32(&active_data[GIT_OPS_LOG], ^uint32(0))
								return
							}
						}
						scanner := bufio.NewScanner(std_out)
						for scanner.Scan() {
							full_author := scanner.Text()
							email := full_author[strings.LastIndex(full_author, "<")+1 : len(full_author)-1]
							select {
							case <-ctx.Done():
								return
							case emails <- email:
								//noop
							}
							select {
							case <-ctx.Done():
								return
							case context_emails <- EmailContext{Repo: &repo, EmailAddress: email, Role: role}:
								//noop
							}
							atomic.AddUint32(&total_data[GIT_IDENTITIES], 1)
						}
						if err = scanner.Err(); err != nil {
							atomic.AddUint32(&error_data[GIT_OPS_LOG], 1)
							atomic.AddUint32(&active_data[GIT_OPS_LOG], ^uint32(0))
							logger.Error(func_logging_name, ": Error scanning text, error: ", err)
							return
						}
						atomic.AddUint32(&completion_data[GIT_OPS_LOG], 1)
						atomic.AddUint32(&active_data[GIT_OPS_LOG], ^uint32(0))
					}(params, role, sem)
				}
			}
		}
	}()
	return emails, context_emails
}

func emails_dedup(emails chan string) (map[string]uint, chan struct{}) {
	emails_deduped := make(map[string]uint, 50)
	done := make(chan struct{})
	go func(emails_deduped map[string]uint) {
		var emails_processed_count uint = 0
		for email := range emails {
			//fmt.Println("Processing email: ", email)
			if _, ok := emails_deduped[email]; !ok {
				emails_deduped[email] = 0
			}
			atomic.AddUint32(&completion_data[EMAILS_DEDUP], 1)
			emails_processed_count++
		}
		close(done)
		logger.Info("Stage 5a - Dedup Emails: Completed. Emails processed: ", emails_processed_count, ". Final email count: ", len(emails_deduped))
	}(emails_deduped)
	return emails_deduped, done
}

func emails_by_repo(contexts chan EmailContext) (map[EmailGroupByRepoKey]int8, chan struct{}) {
	emails_grouped := make(map[EmailGroupByRepoKey]int8, 50)
	done := make(chan struct{})
	go func(emails_grouped map[EmailGroupByRepoKey]int8) {
		var emails_processed_count uint = 0
		for context := range contexts {
			//fmt.Printf("Processing email: %s for %s\n", context.EmailAddress, context.Repo.Name)
			emails_grouped[EmailGroupByRepoKey{Email: context.EmailAddress, Repo: context.Repo}] |= context.Role
			atomic.AddUint32(&completion_data[EMAILS_GROUPED], 1)
			emails_processed_count++
		}
		close(done)
		logger.Info("Stage 5b - Emails per Repo: Completed. Emails processed: ", emails_processed_count, ". Final contextual info count: ", len(emails_grouped))
	}(emails_grouped)
	return emails_grouped, done
}

func create_output_file(output_file string, emails map[string]uint) error {

	output_data := g_buff_pool.Get().(*bytes.Buffer)
	output_data.Reset()
	defer g_buff_pool.Put(output_data)
	for key := range emails {
		output_data.WriteString(key)
		output_data.WriteString(LINE_SEP)
	}
	// Try to write three times before giving up
	var write_counter int8 = 1
	for {
		// Write it as one block
		err := ioutil.WriteFile(output_file, output_data.Bytes(), 0600)
		if err != nil {
			logger.Debug("Create Deduped File: Error writing file, attempt: ", write_counter, ". Error: ", err)
			if write_counter > 3 {
				return err
			}
			write_counter++
			// Short sleep before retrying
			time.Sleep(time.Millisecond * 100)
			continue
		}
		break
	}

	return nil
}

func create_output_json(output_json string, emails_grouped map[EmailGroupByRepoKey]int8) error {

	repos := make(map[string]FmtEmailPerRepo)
	emails := make(map[string]map[string][]FmtRepoPerEmail)

	role_reference := map[int8]string{ROLE_AUTHOR: ROLE_NAME_AUTHOR, ROLE_COMMITTER: ROLE_NAME_COMMITTER, ROLE_MASK_BOTH: ROLE_NAME_BOTH}

	var domain string
	for group_by_key, role_id := range emails_grouped {
		if group_by_key.Email == "" {
			group_by_key.Email = "!blank!"
			domain = "!none!"
		} else {
			at_index := strings.LastIndex(group_by_key.Email, "@")
			if at_index > 0 {
				domain = string(group_by_key.Email[at_index+1:])
			} else {
				domain = "!none!"
			}
		}

		if _, ok := repos[group_by_key.Repo.Name]; !ok {
			repos[group_by_key.Repo.Name] = FmtEmailPerRepo{RepoUrl: group_by_key.Repo.Clone_url, Emails: map[string]string{}}
		}
		if _, ok := emails[domain]; !ok {
			emails[domain] = make(map[string][]FmtRepoPerEmail)
		}
		if _, ok := emails[domain][group_by_key.Email]; !ok {
			emails[domain][group_by_key.Email] = []FmtRepoPerEmail{}
		}

		emails[domain][group_by_key.Email] = append(emails[domain][group_by_key.Email], FmtRepoPerEmail{RepoName: group_by_key.Repo.Name, RepoUrl: group_by_key.Repo.Clone_url, Role: role_reference[role_id]})

		repos[group_by_key.Repo.Name].Emails[group_by_key.Email] = role_reference[role_id]

	}
	output := make(map[string]interface{})
	output["repos"] = repos
	output["emails"] = emails

	b, err := json.MarshalIndent(output, "", "\t")
	if err != nil {
		return err
	}
	var write_counter int8 = 1
	for {
		err = ioutil.WriteFile(output_json, b, 0600)
		if err != nil {
			logger.Debug("Create JSON: Error writing file, attempt: ", write_counter, ". Error: ", err)
			if write_counter > 3 {
				return err
			}
			write_counter++
			time.Sleep(time.Millisecond * 100)
			continue
		}
		break
	}

	return nil
}

func init() {
	if runtime.GOOS == "windows" {
		LINE_SEP = "\r\n"
	} else {
		LINE_SEP = "\n"
	}
}

func init() {
	args, err := parser.Parse()
	if err != nil {
		if flagsErr, ok := err.(*flags.Error); ok && flagsErr.Type == flags.ErrHelp {
			os.Exit(0)
		} else {
			parser.WriteHelp(os.Stderr)
			os.Exit(1)
		}
	}
	if len(args) > 0 {
		fmt.Fprintf(os.Stderr, "Unexpected arguments found: %s\n", strings.Join(args, " "))
		parser.WriteHelp(os.Stderr)
		os.Exit(1)
	}
	if !opts.Resource.User && !opts.Resource.Org && !opts.Resource.Url && len(opts.Resource.Type) == 0 {
		fmt.Fprintln(os.Stderr, "Please provide either org, user or url as the target type")
		parser.WriteHelp(os.Stderr)
		os.Exit(1)
	}
	if opts.Resource.User != opts.Resource.Org != opts.Resource.Url != (len(opts.Resource.Type) == 0) {
		fmt.Fprintln(os.Stderr, "Please use only one setting: --user, --org, --url or --type <user|org|url>")
		parser.WriteHelp(os.Stderr)
		os.Exit(1)
	}
	if opts.Application.Quiet && opts.Application.Verbose {
		fmt.Fprintln(os.Stderr, "You can't have it both ways, quiet and verbose")
		parser.WriteHelp(os.Stderr)
		os.Exit(1)
	}

}

func main() {

	// Set up a global buffer pool for all functions to use
	g_buff_pool = sync.Pool{
		New: func() interface{} {
			return new(bytes.Buffer)
		},
	}

	var (
		err         error
		working_dir string
		target_type string
		git_path    string
		NUM_WORKERS int8
		output_file string
		output_json string
		ok          bool
		size_filter uint64
	)

	if opts.Application.Verbose {
		logger.set_level(LOG_DEBUG)
	} else if opts.Application.Quiet {
		logger.set_level(LOG_ERROR)
	} else {
		logger.set_level(LOG_INFO)
	}
	defer logger.Wait()

	if opts.Application.WorkingDir == "!None-Provided!" {
		working_path, err := os.Getwd()
		if err != nil {
			logger.Fatal(fmt.Sprintf("Working directory not provided and could not retrive a directory to use. Error: %s", err))
		}
		logger.Debugf("Working directory found: %s", working_path)
		opts.Application.WorkingDir = flags.Filename(filepath.Join(working_path, "working_dir"))
		logger.Infof("Working directory not provided, using %s.", opts.Application.WorkingDir)
	}
	if opts.Application.GitPath == "!None-Provided!" {
		path, err := exec.LookPath("git")
		if err != nil {
			logger.Fatal("Git path not provided and could not find `git` in the $PATH.")
		}
		opts.Application.GitPath = flags.Filename(path)
		logger.Infof("Git path not provided, using %s.", opts.Application.GitPath)
	}
	if opts.Advanced.QueueSize < 1 {
		logger.Error("Queue size is too small, resetting to 20")
		opts.Advanced.QueueSize = 20
	}
	if opts.Advanced.Workers < 1 {
		logger.Error("Too few workers assigned, resetting to 20")
		opts.Advanced.Workers = 20
	}
	if opts.Resource.User {
		target_type = "users"
	} else if opts.Resource.Org {
		target_type = "orgs"
	} else if opts.Resource.Url {
		target_type = "url"
	} else {
		switch opts.Resource.Type {
		case "org":
			target_type = "orgs"
		case "user":
			target_type = "users"
		case "url":
			target_type = "url"
		}
	}
	if len(target_type) < 3 {
		logger.Panic("Not actually sure what happened here. Please open a bug report")
	}
	if opts.Resource.SizeFilter <= 0 {
		logger.Info("Disabling size filter for cloning")
		size_filter = 0
	} else {
		size_filter = opts.Resource.SizeFilter
	}

	working_dir = string(opts.Application.WorkingDir)
	output_file = string(opts.Output.OutputFile)
	output_json = string(opts.Output.OutputJson)

	ok, err = check_working_dir(working_dir)
	if !ok {
		if err == nil {
			logger.Fatal(fmt.Sprintf("%v is not empty", working_dir))
		} else {
			logger.Panic(fmt.Sprintf("Cannot use %v. Error: %v", working_dir, err))
		}
	}

	ok, err = check_ouput_location(output_file)
	if !ok {
		logger.Fatal(fmt.Sprintf("Could not create %v. Error: %v", output_file, err))
	}

	ok, err = check_ouput_location(output_json)
	if !ok {
		logger.Fatal(fmt.Sprintf("Could not create %v. Error: %v", output_json, err))
	}

	BUFFER_SIZE = opts.Advanced.QueueSize

	NUM_WORKERS = opts.Advanced.Workers

	var url string
	if target_type != "url" {
		var url_base string = "https://api.github.com/{target-type}/{target-name}/repos?per_page=100"
		r := strings.NewReplacer("{target-type}", target_type, "{target-name}", opts.Args.TargetName)

		// Add the org name to the URL
		url = r.Replace(url_base)
	} else {
		url = opts.Args.TargetName
	}

	//var output_dir string = "/mnt/shared/python/output_dir"

	git_path, err = filepath.Abs(string(opts.Application.GitPath))
	if err != nil {
		logger.Panic(fmt.Sprintf("%v", err))
	}

	// Set up global semaphore for the system
	g_semaphore = semaphore.NewWeighted(int64(NUM_WORKERS))

	logger.Info("Starting...")

	completion_data = make([]uint32, 6)
	error_data = make([]uint32, 6)
	total_data = make([]uint32, 4)
	active_data = make([]uint32, 4)

	// Set up a context to allow for an exit to still write a file
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	github_repo_data := get_repos_from_github(ctx, url)

	repos := parse_github_response(ctx, github_repo_data, opts.Resource.ForkFilter)

	local_repos := git_ops_clone(ctx, repos, &git_path, &working_dir, size_filter)

	emails, contexts := git_ops_shortlog(ctx, local_repos, &git_path)

	emails_deduped, email_list_done := emails_dedup(emails)

	emails_grouped, email_group_done := emails_by_repo(contexts)

	c := make(chan os.Signal, 1)
	signal.Notify(c, os.Interrupt)
	go func() {
		defer signal.Reset()
		select {
		case <-c:
			cancel()
			return
		case <-email_list_done:
			return
		case <-email_group_done:
			return
		}
	}()

	w := new(tabwriter.Writer)
	w.Init(os.Stdout, 0, 4, 1, ' ', 0)

selectloop:
	for {
		select {
		case <-email_list_done:
			break selectloop
		case <-email_group_done:
			break selectloop
		case <-time.After(10 * time.Second):
			fmt.Println("=====START=====")
			fmt.Fprintln(w, "Stage\tActive\tCompleted\tTotal\tErrors\t")
			fmt.Fprintln(w, "Stage 1 - Get Github Repos\t", atomic.LoadUint32(&active_data[GITHUB_FETCH]), "\t", atomic.LoadUint32(&completion_data[GITHUB_FETCH]), "\t", atomic.LoadUint32(&total_data[GITHUB_TOTAL_PAGES]), "\t", atomic.LoadUint32(&error_data[GITHUB_FETCH]), "\t")
			fmt.Fprintln(w, "Stage 2 - Parse URLs\t", atomic.LoadUint32(&active_data[GITHUB_PARSE]), "\t", atomic.LoadUint32(&completion_data[GITHUB_PARSE]), "\t", atomic.LoadUint32(&total_data[GITHUB_TOTAL_PAGES]), "\t", atomic.LoadUint32(&error_data[GITHUB_PARSE]), "\t")
			fmt.Fprintln(w, "Stage 3 - Clone Repos\t", atomic.LoadUint32(&active_data[GIT_OPS_CLONE]), "\t", atomic.LoadUint32(&completion_data[GIT_OPS_CLONE]), "\t", atomic.LoadUint32(&total_data[REMOTE_REPOS]), "\t", atomic.LoadUint32(&error_data[GIT_OPS_CLONE]), "\t")
			fmt.Fprintln(w, "Stage 4 - Find Emails\t", atomic.LoadUint32(&active_data[GIT_OPS_LOG]), "\t", atomic.LoadUint32(&completion_data[GIT_OPS_LOG]), "\t", atomic.LoadUint32(&total_data[LOCAL_REPOS]), "\t", atomic.LoadUint32(&error_data[GIT_OPS_LOG]), "\t")
			fmt.Fprintln(w, "Stage 5a - Dedup Emails\t", "N/A", "\t", atomic.LoadUint32(&completion_data[EMAILS_DEDUP]), "\t", atomic.LoadUint32(&total_data[GIT_IDENTITIES]), "\t", "N/A", "\t")
			fmt.Fprintln(w, "Stage 5b - Emails per Repo\t", "N/A", "\t", atomic.LoadUint32(&completion_data[EMAILS_GROUPED]), "\t", atomic.LoadUint32(&total_data[GIT_IDENTITIES]), "\t", "N/A", "\t")
			w.Flush()
			fmt.Println("=====END=====")
		}
	}
	<-email_list_done
	<-email_group_done
	cancel()
	go func() {
		fmt.Println("=====COMPLETED=====")
		fmt.Fprintln(w, "Stage\tActive\tCompleted\tTotal\tErrors\t")
		fmt.Fprintln(w, "Stage 1 - Get Github Repos\t", atomic.LoadUint32(&active_data[GITHUB_FETCH]), "\t", atomic.LoadUint32(&completion_data[GITHUB_FETCH]), "\t", atomic.LoadUint32(&total_data[GITHUB_TOTAL_PAGES]), "\t", atomic.LoadUint32(&error_data[GITHUB_FETCH]), "\t")
		fmt.Fprintln(w, "Stage 2 - Parse URLs\t", atomic.LoadUint32(&active_data[GITHUB_PARSE]), "\t", atomic.LoadUint32(&completion_data[GITHUB_PARSE]), "\t", atomic.LoadUint32(&total_data[GITHUB_TOTAL_PAGES]), "\t", atomic.LoadUint32(&error_data[GITHUB_PARSE]), "\t")
		fmt.Fprintln(w, "Stage 3 - Clone Repos\t", atomic.LoadUint32(&active_data[GIT_OPS_CLONE]), "\t", atomic.LoadUint32(&completion_data[GIT_OPS_CLONE]), "\t", atomic.LoadUint32(&total_data[REMOTE_REPOS]), "\t", atomic.LoadUint32(&error_data[GIT_OPS_CLONE]), "\t")
		fmt.Fprintln(w, "Stage 4 - Find Emails\t", atomic.LoadUint32(&active_data[GIT_OPS_LOG]), "\t", atomic.LoadUint32(&completion_data[GIT_OPS_LOG]), "\t", atomic.LoadUint32(&total_data[LOCAL_REPOS]), "\t", atomic.LoadUint32(&error_data[GIT_OPS_LOG]), "\t")
		fmt.Fprintln(w, "Stage 5a - Dedup Emails\t", "N/A", "\t", atomic.LoadUint32(&completion_data[EMAILS_DEDUP]), "\t", atomic.LoadUint32(&total_data[GIT_IDENTITIES]), "\t", "N/A", "\t")
		fmt.Fprintln(w, "Stage 5b - Emails per Repo\t", "N/A", "\t", atomic.LoadUint32(&completion_data[EMAILS_GROUPED]), "\t", atomic.LoadUint32(&total_data[GIT_IDENTITIES]), "\t", "N/A", "\t")
		w.Flush()
		fmt.Println("=====COMPLETED=====")
	}()
	var out_files_wg sync.WaitGroup
	defer out_files_wg.Wait()

	out_files_wg.Add(1)
	go func(output_file string, emails map[string]uint) {
		defer out_files_wg.Done()
		if len(emails) == 0 {
			// Nothing to write
			return
		}
		err := create_output_file(output_file, emails)
		if err != nil {
			logger.Error("There was an error: ", err)
			return
		}
		// Sleep for a little to ensure this prints at the end (dirty and if someone can help that would be ideal)
		time.Sleep(250 * time.Millisecond)
		logger.Info("Successfully wrote the file", output_file)
	}(output_file, emails_deduped)

	out_files_wg.Add(1)
	go func(output_json string, emails_grouped map[EmailGroupByRepoKey]int8) {
		defer out_files_wg.Done()
		if len(emails_grouped) == 0 {
			// Nothing to write
			return
		}
		err := create_output_json(output_json, emails_grouped)
		if err != nil {
			logger.Error("There was an error: ", err)
			return
		}
		// Sleep for a little to ensure this prints at the end (dirty and if someone can help that would be ideal)
		time.Sleep(250 * time.Millisecond)
		logger.Info("Successfully wrote the json", output_json)
	}(output_json, emails_grouped)

	if !opts.Application.PreserveDir {
		logger.Info("Clearing working_dir")
		err = os.RemoveAll(working_dir)
		if err != nil {
			logger.Panic(fmt.Sprintf("Could not clear %v. Error: %v", working_dir, err))
		}
	}
}
