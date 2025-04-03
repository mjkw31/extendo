/*
 * Copyright (C) 2019, 2020, 2021, 2022. Genome Research Ltd. All rights
 * reserved.
 *
 * This program is free software: you can redistribute it and/or modify
 * it under the terms of the GNU General Public License as published by
 * the Free Software Foundation, either version 3 of the License,
 * (at your option) any later version.
 *
 * This program is distributed in the hope that it will be useful,
 * but WITHOUT ANY WARRANTY; without even the implied warranty of
 * MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
 * GNU General Public License for more details.
 *
 * You should have received a copy of the GNU General Public License
 * along with this program.  If not, see <http://www.gnu.org/licenses/>.
 *
 * @file client.go
 * @author Keith James <kdj@sanger.ac.uk>
 */

package extendo

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/pkg/errors"
	logs "github.com/wtsi-npg/logshim"
)

const (
	CHMOD     = "chmod"     // chmod baton operation
	CHECKSUM  = "checksum"  // checksum baton operation
	GET       = "get"       // get baton operation
	LIST      = "list"      // list baton operation
	METAMOD   = "metamod"   // metamod baton operation
	METAADD   = "add"       // metamod add baton operation
	METAREM   = "rem"       // metamod rem baton operation
	METAQUERY = "metaquery" // metaquery baton operation
	MKDIR     = "mkdir"     // mkdir baton operation
	PUT       = "put"       // put baton operation
	REMOVE    = "remove"    // rm baton operation
	RMDIR     = "rmdir"     // rmdir baton operation
)

const (
	RodsUserFileDoesNotExist  = int32(-310000) // iRODS: user file does not exist
	RodsCatCollectionNotEmpty = int32(-821000) // iRODS: collection not empty
	RodsUnixFileReadError     = int32(-512021) // iRODS: failed to read a file
)

// DefaultResponseTimeout is a timeout for the baton-do sub-process to respond
// or confirm that it is still  running. Significant response times can be real,
// for example responding after a put operation on 1 TiB of data.
var DefaultResponseTimeout = 5 * time.Second

// Client is a launcher for a baton sub-process which holds its system I/O
// streams and its channels. If accessed from more than one goroutine,
// instances must be externally synchronised.
type Client struct {
	path         string             // Path of the baton executable.
	cmd          *exec.Cmd          // Cmd of the sub-process, once started.
	stdin        io.WriteCloser     // stdin of the sub-process, once started.
	stdout       io.ReadCloser      // stdout of the sub-process, once started.
	stderr       io.ReadCloser      // stderr of the sub-process, once started.
	in           chan []byte        // For sending to the sub-process.
	err          chan error         // For recording any sub-process error.
	pid          int                // PID of the sub-process.
	respTimeout  time.Duration      // Timeout for the sub-process to respond.
	cancel       context.CancelFunc // For stopping the I/O goroutines.
	inWaitGroup  *sync.WaitGroup    // WaitGroup for STDIN goroutine.
	outWaitGroup *sync.WaitGroup    // WaitGroup for STDOUT/STDERR goroutines.
	sync.RWMutex
	out          map[uint64]chan<- []byte // For receiving from the sub-process.
	isRunning    bool                     // Flag indicating that the sub-process is running.
	startTime    time.Time                // Time at which the sub-process was started.
	stopTime     time.Time                // Time at which the sub-process completed.
	activityTime time.Time                // Time of the last activity. Updated by execute().
	nextID       uint64
}

type envelopeID struct {
	// Client-unique request ID
	ID uint64
}

// Envelope is the JSON document accepted by baton-do, describing an operation
// to perform on a target. It is also the document returned by baton-do
// afterwards, describing the outcome of the operation, including return value
// and errors.
type Envelope struct {
	envelopeID
	// Operation for baton-do.
	Operation string `json:"operation"`
	// Arguments for operation.
	Arguments Args `json:"arguments"`
	// Target of the operation.
	Target RodsItem `json:"target"`
	// Result of the operation.
	Result *ResultWrapper `json:"result,omitempty"`
	// ErrorMsg from the operation.
	ErrorMsg *ErrorMsg `json:"error,omitempty"`
}

// Args contains the arguments for the various baton-do operation parameters.
type Args struct {
	// Request an operation.
	Operation string `json:"operation,omitempty"`
	// Request ACLs.
	ACL bool `json:"acl,omitempty"`
	// Request metadata AVUs.
	AVU bool `json:"avu,omitempty"`
	// Request checksums.
	Checksum bool `json:"checksum,omitempty"`
	// Request checksums are verified on put.
	Verify bool `json:"verify,omitempty"`
	// Restrict to collections.
	Collection bool `json:"collection,omitempty"`
	// Request collection contents.
	Contents bool `json:"contents,omitempty"`
	// Force an operation.
	Force bool `json:"force,omitempty"`
	// Restrict to data objects.
	Object bool `json:"object,omitempty"`
	// Request a recursive operation.
	Recurse bool `json:"recurse,omitempty"`
	// Request replicate information.
	Replicate bool `json:"replicate,omitempty"`
	// Request data object size.
	Size bool `json:"size,omitempty"`
	// Request timestamps.
	Timestamp bool `json:"timestamp,omitempty"`
}

// ResultWrapper allows handling of both single results and lists of results in
// JSON.
type ResultWrapper struct {
	Item *RodsItem   `json:"single,omitempty"`
	List *[]RodsItem `json:"multiple,omitempty"`
}

// ErrorMsg allows handling of an iRODS error message in JSON.
type ErrorMsg struct {
	Message string `json:"message"`
	Code    int32  `json:"code"`
}

// RodsError is an error raised on the iRODS server and reported to the client.
type RodsError struct {
	err  error
	code int32
}

// Error implements the error interface for RodsErrors.
func (e *RodsError) Error() string {
	return fmt.Sprintf("%s code: %d", e.err, e.code)
}

// Code returns the iRODS error code for an error.
func (e *RodsError) Code() int32 {
	return e.code
}

// IsRodsError returns true if the Cause of the error is a RodsError.
func IsRodsError(err error) bool {
	switch errors.Cause(err).(type) {
	case *RodsError:
		return true
	default:
		return false
	}
}

// RodsErrorCode returns the iRODS error code of the Cause error. If the cause
// is not a RodsError, an error is returned.
func RodsErrorCode(err error) (int32, error) {
	switch e := errors.Cause(err).(type) {
	case *RodsError:
		return e.Code(), nil
	default:
		return int32(0),
			errors.Errorf("cannot get an iRODS error code from error: '%v'", err)
	}
}

// FindBaton returns the cleaned path to the first occurrence of the baton-do
// executable in the environment's PATH. If the executable is not found, an
// error is raised.
func FindBaton() (string, error) {
	var baton string
	var err error

	envPath := os.Getenv("PATH")
	dirs := strings.Split(envPath, ":")

	for _, dir := range dirs {
		paths, err := filepath.Glob(filepath.Join(dir, "baton-do"))
		if err != nil {
			break
		}

		for _, path := range paths {
			if filepath.Base(path) == "baton-do" {
				baton = path
				break
			}
		}
	}
	if baton == "" {
		return baton, errors.Errorf("baton-do not present in PATH '%s'",
			envPath)
	}

	return filepath.Clean(baton), err
}

// BatonVersion reports the version string printed by baton-do --version
func BatonVersion() (string, error) {
	baton, err := FindBaton()
	if err != nil {
		return baton, err
	}

	cmd := exec.Command(baton, "--version")
	var out bytes.Buffer
	cmd.Stdout = &out

	err = cmd.Run()
	if err != nil {
		return baton, err
	}

	return strings.TrimSpace(out.String()), err
}

// FindAndStart locates the baton-do executable using FindBaton, creates a
// Client using NewClient and finally calls Start on the newly created Client,
// passing the argument strings of this function to the Start method. The
// running Client is returned.
func FindAndStart(arg ...string) (*Client, error) {
	baton, err := FindBaton()
	if err != nil {
		return nil, err
	}

	client, err := NewClient(baton)
	if err != nil {
		return nil, err
	}

	return client.Start(arg...)
}

// NewClient returns a new instance with the executable path set. The path
// argument is passed to exec.LookPath.
func NewClient(path string) (*Client, error) {
	executable, err := exec.LookPath(path)
	if err != nil {
		return nil, err
	}

	return &Client{path: executable}, err
}

// Start runs the client's external baton program, creating new channels for
// communication with it. The arguments to Start are passed as command line
// arguments to the baton program. If the program is already running and Start
// is called, an error is raised.
func (client *Client) Start(arg ...string) (*Client, error) {
	client.Lock()
	defer client.Unlock()

	if client.isRunning {
		return client, errors.New("client is already running")
	}

	log := logs.GetLogger()

	cmd := exec.Command(client.path, arg...)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, err
	}

	// Start in its own process group. This allows a more graceful shutdown
	// when stopped with ^C in the shell. The baton-do process won't get the
	// SIGINT and will be stopped by the parent process.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true, Pgid: 0}

	if err = cmd.Start(); err != nil {
		return nil, err
	}

	// Sub-process input and response synchronisation
	pin := make(chan []byte)
	perr := make(chan error, 1)

	// I/O goroutine cancelling and cleanup
	cancelCtx, cancel := context.WithCancel(context.Background())

	var inWg, outWg sync.WaitGroup
	inWg.Add(1)
	outWg.Add(2) // The stdout, stderr goroutines

	// Send messages to the baton sub-process
	go func(ctx context.Context) {
		defer inWg.Done()

		for {
			select {
			case <-ctx.Done():
				// Close stdin to unblock the reader
				if ce := stdin.Close(); ce != nil {
					log.Error().Err(ce).Str("executable", client.path).
						Msg("failed to close stdin")
				}
				return
			case buf := <-pin:
				n, werr := stdin.Write(append(buf, '\n'))
				if werr != nil {
					log.Error().Err(werr).
						Str("executable", client.path).
						Str("value", string(buf)).
						Int("num_written", n).
						Msg("error writing to stdin")
				}
			}
		}
	}(cancelCtx)

	// Receive messages from the baton sub-process STDOUT
	go func(ctx context.Context) {
		defer outWg.Done()
		defer func() {
			client.Lock()

			for _, ch := range client.out {
				close(ch)
			}

			client.Unlock()
		}()

		rd := bufio.NewReader(stdout)

		for {
			select {
			case <-ctx.Done():
				return
			default:
				// On cancelling, this is unblocked by the "send" goroutine
				// closing stdin, and we should reach EOF
				bout, re := rd.ReadBytes('\n')
				if re == nil {
					resp := bytes.TrimRight(bout, "\r\n")

					var envID envelopeID
					if err := json.Unmarshal(resp, &envID); err != nil {
						continue
					}

					client.Lock()
					pout, ok := client.out[envID.ID]
					delete(client.out, envID.ID)
					client.Unlock()

					if !ok {
						continue
					}

					pout <- resp
				} else if errors.Is(re, io.EOF) {
					log.Debug().Str("executable", client.path).
						Msg("reached EOF on stdout")
					return
				} else {
					log.Error().Err(re).Str("executable", client.path).
						Msg("read error on stdout")
					return
				}
			}
		}
	}(cancelCtx)

	// Handle baton sub-process STDERR
	go func(ctx context.Context) {
		defer outWg.Done()

		rd := bufio.NewReader(stderr)

		for {
			select {
			case <-ctx.Done():
				return
			default:
				// On cancelling, this is unblocked by the "send" goroutine
				// closing stdin, and we should reach EOF
				bout, re := rd.ReadBytes('\n')
				if re == nil {
					out := bytes.TrimRight(bout, "\r\n")
					log.Debug().Str("stderr", client.path).
						Msg(string(out))
				} else if errors.Is(re, io.EOF) {
					log.Debug().Str("executable", client.path).
						Msg("reached EOF on stderr")
					return
				} else {
					log.Error().Err(re).Str("executable", client.path).
						Msg("read error on stderr")
					return
				}
			}
		}
	}(cancelCtx)

	client.cmd = cmd
	client.stdin = stdin
	client.stdout = stdout
	client.stderr = stderr
	client.in = pin
	client.out = make(map[uint64]chan<- []byte)
	client.err = perr
	client.pid = cmd.Process.Pid
	client.isRunning = true
	client.startTime = time.Now()
	client.respTimeout = DefaultResponseTimeout
	client.cancel = cancel
	client.inWaitGroup = &inWg
	client.outWaitGroup = &outWg

	// Watch for baton sub-process completion and capture any error
	go func() {
		outWg.Wait()
		err := cmd.Wait()

		client.Lock()
		client.isRunning = false
		client.stopTime = time.Now()
		client.Unlock()

		inWg.Wait()

		client.err <- err
		close(client.err)
	}()

	return client, err
}

// Stop stops the baton sub-process, if it is running. Returns any error
// from the sub-process.
func (client *Client) Stop() error {
	client.cancel()

	return <-client.err
}

// IdleTime returns the duration for which the client has been idle (time
// elapsed since the last request sent to the sub-process). If the client is no
// longer, running returns the time since the client stopped.
func (client *Client) IdleTime() time.Duration {
	client.RLock()
	defer client.RUnlock()

	if client.isRunning {
		return time.Since(client.activityTime)
	}
	return client.stopTime.Sub(client.activityTime)
}

// Runtime returns the duration for which the client has run. If the client is
// running, it reports time spent so far. If the client has been stopped, it
// reports the duration for which it ran.
func (client *Client) Runtime() time.Duration {
	client.RLock()
	defer client.RUnlock()

	if client.isRunning {
		return time.Since(client.startTime)
	}
	return client.stopTime.Sub(client.startTime)
}

// StopIgnoreError stops the baton sub-process, if it is running. Ignores any
// error from the sub-process.
func (client *Client) StopIgnoreError() {
	if err := client.Stop(); err != nil {
		logs.GetLogger().Error().Err(err).Int("pid", client.ClientPid()).
			Msg("stopped client")
	}
}

// ClientPid returns the process ID of the baton sub-process if it has started,
// or -1 otherwise.
func (client *Client) ClientPid() int {
	client.RLock()
	defer client.RUnlock()

	if client.isRunning {
		return client.pid
	}
	return -1
}

// IsRunning returns true if the client's baton sub-process is running.
func (client *Client) IsRunning() bool {
	client.RLock()
	defer client.RUnlock()

	return client.isRunning
}

// Chmod sets permissions on a collection or data object in iRODS. By setting
// Args.Recurse=true, the operation may be made recursive.
func (client *Client) Chmod(args Args, item RodsItem) (RodsItem, error) {
	items, err := client.execute(CHMOD, args, item)
	if err != nil {
		return item, err
	}
	return items[0], err
}

// Checksum calculates a checksum for a data object in iRODS. iRODS makes this
// a no-op if a checksum is already recorded. However, this can be overridden
// by setting Force=true in Args. When called, this sets or updates the checksum
// on all replicates. If Args.Checksum=true is set, the new checksum will
// be reported in the return value.
func (client *Client) Checksum(args Args, item RodsItem) (RodsItem, error) {
	items, err := client.execute(CHECKSUM, args, item)
	if err != nil {
		return item, err
	}

	return items[0], err
}

// Get fetches a data object from iRODS. Fetching collections recursively is
// not supported.
func (client *Client) Get(args Args, item RodsItem) (RodsItem, error) {
	items, err := client.execute(GET, args, item)
	if err != nil {
		return item, err
	}
	return items[0], err
}

// List retrieves information about collections and/or data objects in iRODS.
// The items returned are sorted (collections first, then by path and finally
// by name). The detailed composition of the items is influenced by the
// supplied Args:
//
// Args.ACL = true        Include ACLs
// Args.AVU = true        Include AVUs
// Args.Contents = true   Include collection direct contents
// Args.Recurse = true    Recurse into collections
// Args.Replicates = true Include replicates for data objects
// Args.Size = true       Include size for data objects
// Args.Timestamp = true  Include timestamps for data objects
func (client *Client) List(args Args, item RodsItem) ([]RodsItem, error) {
	if args.Recurse {
		return client.listRecurse(args, item)
	}

	return client.execute(LIST, args, item)
}

// ListItem retrieves information about an individual collection or data
// object in iRODS. The effects of Args are the same as for List, except that
// Recurse is not permitted. If the listed item does not exist, an error is
// returned. If the operation would return more than one collection or data
// object, an error is returned.
func (client *Client) ListItem(args Args, item RodsItem) (RodsItem, error) {
	if args.Recurse {
		return item, errors.New("invalid argument: Recurse=true")
	}

	items, err := client.execute(LIST, args, item)
	if err != nil {
		return item, err
	}

	switch len(items) {
	case 0:
		return item, errors.Errorf("no such item: %+v", item)
	case 1:
		return items[0], err
	default:
		return item, errors.Errorf("attempt to ListItem multiple "+
			"items: %+v", items)
	}
}

// ListChecksum returns the iRODS checksum of an item, which must be a data
// object.
func (client *Client) ListChecksum(item RodsItem) (string, error) {
	var checksum string

	if !item.IsDataObject() {
		return checksum, errors.Errorf("invalid argument: can only get "+
			"the checksum of a file or data object, but was passed %+v", item)
	}

	obj, err := client.ListItem(Args{Checksum: true}, item)
	if err != nil {
		return checksum, err
	}
	checksum = obj.IChecksum

	return checksum, err
}

func (client *Client) metaMod(args Args, item RodsItem) (RodsItem, error) {
	items, err := client.execute(METAMOD, args, item)
	if err != nil {
		return item, err
	}
	return items[0], err
}

// MetaAdd adds the AVUs of the item to a collection or data object in iRODS
// and returns the item.
func (client *Client) MetaAdd(args Args, item RodsItem) (RodsItem, error) {
	args.Operation = METAADD
	return client.metaMod(args, item)
}

// MetaRem removes the AVUs of the item from a collection or data object in
// iRODS and returns the item.
func (client *Client) MetaRem(args Args, item RodsItem) (RodsItem, error) {
	args.Operation = METAREM
	return client.metaMod(args, item)
}

// MetaQuery runs a metadata search in iRODS. The query scope must be set in
// Args by setting Args.Object = true (for data objects) and/or
// Args.Collection = true (for collections). The iRODS zone for the query may
// be set by providing a root iRODS path in the RodsItem to act as a zone hint.
// e.g. RodsItem.IPath = "/seq".
func (client *Client) MetaQuery(args Args, item RodsItem) ([]RodsItem, error) {
	if !(args.Object || args.Collection) {
		return nil, errors.Errorf("metaquery arguments must specify " +
			"Object and/or Collection targets; neither were specified")
	}

	return client.execute(METAQUERY, args, item)
}

// MkDir creates a new collection in iRODS and returns the item.
func (client *Client) MkDir(args Args, item RodsItem) (RodsItem, error) {
	items, err := client.execute(MKDIR, args, item)
	if err != nil {
		return item, err
	}
	return items[0], err
}

// Put a collection or data object into iRODS and returns the item. By
// setting Args.Recurse=true, the operation may be made recursive on a
// collection.
func (client *Client) Put(args Args, item RodsItem) ([]RodsItem, error) {
	if args.Recurse {
		return client.putRecurse(args, item)
	}

	return client.execute(PUT, args, item)
}

// RemObj removes a data object from iRODS and returns the item.
func (client *Client) RemObj(args Args, item RodsItem) ([]RodsItem, error) {
	return client.execute(REMOVE, args, item)
}

// RemDir removes a collection from iRODS and returns the item.
func (client *Client) RemDir(args Args, item RodsItem) ([]RodsItem, error) {
	return client.execute(RMDIR, args, item)
}

func (client *Client) listRecurse(args Args, item RodsItem) ([]RodsItem, error) {
	var items []RodsItem

	if item.IsDataObject() {
		item.client = client
		return []RodsItem{item}, nil
	}

	items = append(items, item)

	args.Contents = true
	populated, err := client.execute(LIST, args, item)
	if err == nil {
		for _, elt := range populated[0].IContents {
			if elt.IsCollection() {
				content, err := client.listRecurse(args, elt)
				if err != nil {
					break
				}

				items = append(items, content...)
			} else {
				items = append(items, elt)
			}
		}
	}
	SortRodsItems(items)

	for i := range items {
		items[i].client = client
	}

	return items, err
}

func (client *Client) putRecurse(args Args, item RodsItem) ([]RodsItem, error) {
	var newItems []RodsItem

	// It is just a simple data object
	if item.IsLocalFile() && (item.IsDataObject() || item.IsCollection()) {
		return client.execute(PUT, args, item)
	}

	if !item.IsLocalDir() {
		return newItems, errors.Errorf("cannot recursively put %s "+
			"into %s because the former is not a directory",
			item.LocalPath(), item.RodsPath())
	}
	if !item.IsCollection() {
		return newItems, errors.Errorf("cannot recursively put %s "+
			"into %s because the latter is not a collection",
			item.LocalPath(), item.RodsPath())
	}

	log := logs.GetLogger()
	rodsRoot := item.RodsPath()

	walkFn := func(path string, info os.FileInfo, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				log.Warn().Err(err).Str("path", path).
					Msg("file was deleted")
				return nil
			}

			return err
		}

		if !info.IsDir() {
			dir := filepath.Dir(path)
			obj := RodsItem{
				client:     client,
				IDirectory: dir,
				IFile:      info.Name(),
				IPath:      filepath.Clean(filepath.Join(rodsRoot, dir)),
				IName:      info.Name()}
			newItems = append(newItems, obj)
		}

		return err
	}

	werr := filepath.Walk(item.LocalPath(), walkFn)
	if werr != nil {
		return newItems, werr
	}

	for i, elt := range newItems {
		// Create the leading collections, if they are not there
		coll := RodsItem{IPath: elt.IPath}
		_, cerr := client.execute(MKDIR, Args{Recurse: true}, coll)
		if cerr != nil {
			return newItems, cerr
		}

		// Put the data object
		objs, oerr := client.execute(PUT, args, elt)
		if oerr != nil {
			return newItems, oerr
		}

		// Update newItems with a populated item
		newItems[i] = objs[0]
	}

	return newItems, nil
}

// execute sends JSON to a baton-do process, which in turn passes a request to
// the iRODS server. This method handles write locking while a call to the
// iRODS server is being run.
func (client *Client) execute(op string, args Args, item RodsItem) ([]RodsItem,
	error) {
	if !client.IsRunning() {
		return []RodsItem{}, errors.New("client is not running")
	}

	out := make(chan []byte, 1)

	client.Lock()
	client.activityTime = time.Now()
	client.nextID++
	id := client.nextID
	client.out[id] = out
	client.Unlock()

	response, err := client.send(wrap(op, args, item, id), out)
	if err != nil {
		return nil, err
	}

	return unwrap(client, response)
}

func (client *Client) send(envelope *Envelope, out <-chan []byte) (*Envelope, error) {
	log := logs.GetLogger()

	jsonMessage, err := json.Marshal(envelope)
	if err != nil {
		return nil, err
	}

	log.Debug().Msgf("Sending %s", jsonMessage)
	client.in <- jsonMessage

	var jsonResponse []byte

waitResponse:
	for {
		select {
		case jsonResponse = <-out:
			log.Debug().Msgf("Received %s", jsonResponse)
			break waitResponse

		case <-time.After(client.respTimeout):
			// If the sub-process is running we just wait again, until either
			// it responds or stops running.
			if !client.IsRunning() {
				ps := client.cmd.ProcessState
				return nil, errors.Errorf("receiving failed because the client stopped. "+
					"PID: %d, process state: %s", ps.Pid(), ps.String())
			}

			log.Debug().Str("executable", client.path).Int("pid", client.ClientPid()).
				Msg("receiving timed out, waiting again ...")
		}
	}

	response := &Envelope{}
	if err = json.Unmarshal(jsonResponse, response); err != nil {
		return nil, err
	}

	return response, err
}

// wrap adds the JSON envelope to the iRODS operation. See the baton-do
// documentation for details.
func wrap(operation string, args Args, target RodsItem, id uint64) *Envelope {
	return &Envelope{
		envelopeID: envelopeID{ID: id},
		Operation:  operation,
		Arguments:  args,
		Target:     target,
	}
}

// unwrap removes the envelope from JSON returned by baton-do and returns any
// RodsItems or error from the iRODS operation.
func unwrap(client *Client, envelope *Envelope) ([]RodsItem, error) {
	var items []RodsItem
	if envelope.ErrorMsg != nil {
		re := RodsError{errors.New(envelope.ErrorMsg.Message),
			envelope.ErrorMsg.Code}

		return items, errors.Wrapf(&re, "%s operation failed",
			envelope.Operation)
	}

	if envelope.Result == nil {
		return items, errors.Errorf("invalid %s operation "+
			"envelope (no result)", envelope.Operation)
	}

	switch {
	case envelope.Result.List != nil:
		items = *envelope.Result.List
	case envelope.Result.Item != nil:
		items = []RodsItem{*envelope.Result.Item}
	default:
		return items, errors.Errorf("invalid %s operation "+
			"result (no content)", envelope.Operation)
	}

	SortRodsItems(items)

	for i := range items {
		items[i].client = client

		var contents = items[i].IContents
		for j := range contents {
			contents[j].client = client
		}
		SortRodsItems(contents)
		items[i].IContents = contents

		var reps = items[i].IReplicates
		SortReplicates(reps)
		items[i].IReplicates = reps

		var avus = items[i].IAVUs
		SortAVUs(avus)
		items[i].IAVUs = avus

		var acls = items[i].IACLs
		SortACLs(acls)
		items[i].IACLs = acls

		var timestamps = items[i].ITimestamps
		SortTimestamps(timestamps)
		items[i].ITimestamps = timestamps
	}

	return items, nil
}
