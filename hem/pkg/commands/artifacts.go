package commands

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"

	"james/hem/pkg/protocol"
	"james/hem/pkg/store"
	"james/moneypenny/pkg/artifact"
	"james/moneypenny/pkg/envelope"
)

// ListArtifacts lists a session and its project, or just a project.
func (e *Executor) ListArtifacts(args []string) *protocol.Response {
	if len(args) != 1 || args[0] == "" {
		return protocol.ErrResponse("usage: list artifact SESSION_ID")
	}
	if project, ok := strings.CutPrefix(args[0], artifact.ProjectScope); ok {
		p, err := e.store.GetProject(project)
		if err != nil {
			return protocol.ErrResponse(err.Error())
		}
		if p == nil || p.ID != project {
			return protocol.ErrResponse("project not found")
		}
		return artifactResponse(e.store.ListProjectArtifacts(project, ""))
	}
	return e.listSessionArtifacts(args[0], nil)
}

// DeleteArtifact removes a published snapshot from its owning session.
func (e *Executor) DeleteArtifact(args []string) *protocol.Response {
	if len(args) != 2 || args[0] == "" || args[1] == "" {
		return protocol.ErrResponse("usage: delete artifact SESSION_ID ARTIFACT_ID")
	}
	request := artifact.Ref{SessionID: args[0], ID: args[1]}
	if project, ok := strings.CutPrefix(request.SessionID, artifact.ProjectScope); ok {
		return artifactResponse(request, e.store.DeleteProjectArtifact(project, "", request.ID))
	}
	return e.relayArtifact(request.SessionID, "delete_artifact", request)
}

// ReadArtifact relays a bounded chunk from the owning MP without storing it.
func (e *Executor) ReadArtifact(args []string) *protocol.Response {
	if len(args) != 1 {
		return protocol.ErrResponse("read artifact requires one JSON request")
	}
	var request artifact.ReadRequest
	if err := decodeGadgetData([]byte(args[0]), &request); err != nil {
		return protocol.ErrResponse(err.Error())
	}
	if project, ok := strings.CutPrefix(request.SessionID, artifact.ProjectScope); ok {
		return artifactResponse(e.store.ReadProjectArtifact(project, "", request))
	}
	return e.relayArtifact(request.SessionID, "read_artifact", request)
}

func artifactResponse[T any](data T, err error) *protocol.Response {
	if err != nil {
		return protocol.ErrResponse(err.Error())
	}
	return protocol.OKResponse(data)
}

func (e *Executor) listSessionArtifacts(id string, caller *store.Session) *protocol.Response {
	target, err := e.store.GetSession(id)
	if err != nil {
		return protocol.ErrResponse(err.Error())
	}
	if target == nil {
		return protocol.ErrResponse("artifact session is not tracked by this Hem")
	}
	response := e.relayArtifact(id, "list_artifacts", envelope.SessionIDData{SessionID: id})
	if response.Status != "ok" || target.ProjectID == "" || (caller != nil && caller.ProjectID != target.ProjectID) {
		return response
	}
	var personal []artifact.Metadata
	if err := json.Unmarshal(response.Data, &personal); err != nil {
		return protocol.ErrResponse("invalid session artifact listing")
	}
	callerID := ""
	if caller != nil {
		callerID = caller.SessionID
	}
	project, err := e.store.ListProjectArtifacts(target.ProjectID, callerID)
	if err != nil {
		return protocol.ErrResponse(err.Error())
	}
	if len(project) == 0 {
		return response
	}
	return protocol.OKResponse(append(personal, project...))
}

func (e *Executor) artifactGadget(source *store.Session, method string, data json.RawMessage) *protocol.Response {
	checkTarget := func(target *string) error {
		if *target == "" {
			*target = source.SessionID
		}
		if *target == source.SessionID {
			return nil
		}
		return e.checkPeerArtifactAccess(source)
	}
	requireProject := func(enabled bool, target string) error {
		if !enabled || target != "" || source.ProjectID == "" {
			return fmt.Errorf("project artifacts require --project and membership in the current project")
		}
		return nil
	}
	switch method {
	case "artifacts.list":
		var request artifact.ListRequest
		if err := decodeGadgetData(data, &request); err != nil {
			return protocol.ErrResponse(err.Error())
		}
		if request.Project {
			if err := requireProject(true, request.SessionID); err != nil {
				return protocol.ErrResponse(err.Error())
			}
			return artifactResponse(e.store.ListProjectArtifacts(source.ProjectID, source.SessionID))
		}
		if err := checkTarget(&request.SessionID); err != nil {
			return protocol.ErrResponse(err.Error())
		}
		return e.listSessionArtifacts(request.SessionID, source)
	case "artifacts.download":
		var request artifact.ReadRequest
		if err := decodeGadgetData(data, &request); err != nil {
			return protocol.ErrResponse(err.Error())
		}
		if request.Project {
			if err := requireProject(true, request.SessionID); err != nil {
				return protocol.ErrResponse(err.Error())
			}
			return artifactResponse(e.store.ReadProjectArtifact(source.ProjectID, source.SessionID, request))
		}
		if err := checkTarget(&request.SessionID); err != nil {
			return protocol.ErrResponse(err.Error())
		}
		return e.relayArtifact(request.SessionID, "read_artifact", request)
	case "artifacts.publish":
		var request artifact.PublishRequest
		if err := decodeGadgetData(data, &request); err != nil {
			return protocol.ErrResponse(err.Error())
		}
		if err := requireProject(request.Project, ""); err != nil {
			return protocol.ErrResponse(err.Error())
		}
		return artifactResponse(e.saveProjectArtifact(source, "", request.Path, request.Title))
	case "artifacts.update":
		var request artifact.UpdateRequest
		if err := decodeGadgetData(data, &request); err != nil {
			return protocol.ErrResponse(err.Error())
		}
		if request.ID == "" {
			return protocol.ErrResponse("artifact ID is required")
		}
		if err := requireProject(request.Project, ""); err != nil {
			return protocol.ErrResponse(err.Error())
		}
		return artifactResponse(e.saveProjectArtifact(source, request.ID, request.Path, ""))
	case "artifacts.delete":
		var request artifact.DeleteRequest
		if err := decodeGadgetData(data, &request); err != nil {
			return protocol.ErrResponse(err.Error())
		}
		if err := requireProject(request.Project, ""); err != nil {
			return protocol.ErrResponse(err.Error())
		}
		return artifactResponse(request, e.store.DeleteProjectArtifact(source.ProjectID, source.SessionID, request.ID))
	default:
		return protocol.ErrResponse("unsupported artifact gadget")
	}
}

func (e *Executor) checkPeerArtifactAccess(source *store.Session) error {
	caps, err := e.currentGadgetCapabilities(source)
	if err != nil {
		return err
	}
	if !caps.Agents {
		return fmt.Errorf("all-agent messaging capability is disabled")
	}
	return nil
}

func (e *Executor) saveProjectArtifact(source *store.Session, id, path, title string) (artifact.Metadata, error) {
	var meta artifact.Metadata
	if !e.projectArtifactUpload.TryLock() {
		return meta, fmt.Errorf("another project artifact transfer is in progress; try again shortly")
	}
	defer e.projectArtifactUpload.Unlock()
	request := artifact.StageRequest{SessionID: source.SessionID, Path: path, Title: title}
	if id != "" {
		previous, err := e.store.ReadProjectArtifact(source.ProjectID, source.SessionID, artifact.ReadRequest{ID: id, MetadataOnly: true})
		if err != nil {
			return meta, err
		}
		meta = previous.Artifact
		request.Name = meta.Name
	}
	mp, err := e.store.GetMoneypenny(source.MoneypennyName)
	if err != nil {
		return meta, err
	}
	if mp == nil {
		return meta, fmt.Errorf("artifact source host is no longer registered")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	staged, err := e.sendCommand(ctx, mp, "stage_artifact", request)
	if err != nil {
		return meta, err
	}
	var snapshot artifact.Metadata
	if err := json.Unmarshal(staged.Data, &snapshot); err != nil || snapshot.ID == "" {
		return meta, fmt.Errorf("invalid staged artifact metadata")
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if _, err := e.sendCommand(cleanup, mp, "delete_staged_artifact", artifact.Ref{SessionID: source.SessionID, ID: snapshot.ID}); err != nil {
			log.Printf("failed to clean staged artifact %s on %q: %v", snapshot.ID, mp.Name, err)
		}
	}()
	if snapshot.Size < 0 || snapshot.Size > artifact.MaxFileSize {
		return meta, fmt.Errorf("invalid staged artifact size")
	}
	if id != "" && snapshot.View != meta.View {
		return meta, fmt.Errorf("replacement must keep the original artifact's preview format")
	}
	content := make([]byte, 0, snapshot.Size)
	for int64(len(content)) < snapshot.Size {
		offset := int64(len(content))
		reply, err := e.sendCommand(ctx, mp, "read_staged_artifact", artifact.ReadRequest{SessionID: source.SessionID, ID: snapshot.ID, Offset: offset})
		if err != nil {
			return meta, err
		}
		var chunk artifact.Chunk
		if err := json.Unmarshal(reply.Data, &chunk); err != nil || chunk.Artifact != snapshot ||
			len(chunk.Content) == 0 || len(chunk.Content) > artifact.StageChunkSize || int64(len(chunk.Content)) > snapshot.Size-offset {
			return meta, fmt.Errorf("invalid staged artifact chunk")
		}
		content = append(content, chunk.Content...)
	}
	sum := sha256.Sum256(content)
	if hex.EncodeToString(sum[:]) != snapshot.SHA256 {
		return meta, fmt.Errorf("staged artifact checksum mismatch")
	}
	if id == "" {
		meta = snapshot
		meta.ID = generateSessionID()
		meta.ProjectID = source.ProjectID
		meta.SentAt = time.Now().UTC().Format(time.RFC3339)
	}
	meta.Size, meta.SHA256 = snapshot.Size, snapshot.SHA256
	return meta, e.store.SaveProjectArtifact(ctx, source.SessionID, meta, content, id != "")
}

func (e *Executor) relayArtifact(sessionID, method string, request any) *protocol.Response {
	session, err := e.store.GetSession(sessionID)
	if err != nil {
		return protocol.ErrResponse(err.Error())
	}
	if session == nil {
		return protocol.ErrResponse("artifact session is not tracked by this Hem")
	}
	mp, err := e.store.GetMoneypenny(session.MoneypennyName)
	if err != nil {
		return protocol.ErrResponse(err.Error())
	}
	if mp == nil {
		return protocol.ErrResponse("artifact host is no longer registered")
	}
	resp, err := e.sendCommand(context.Background(), mp, method, request)
	if err != nil {
		return protocol.ErrResponse(err.Error())
	}
	if !json.Valid(resp.Data) {
		return protocol.ErrResponse(fmt.Sprintf("invalid response from %s", method))
	}
	return protocol.OKResponse(resp.Data)
}
