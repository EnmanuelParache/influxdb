package http

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"path"
	"time"

	"github.com/influxdata/httprouter"
	"github.com/influxdata/influxdb"
	pctx "github.com/influxdata/influxdb/context"
	"github.com/influxdata/influxdb/notification/rule"
	"github.com/influxdata/influxdb/pkg/httpc"
	"go.uber.org/zap"
)

var _ influxdb.NotificationRuleStore = (*NotificationRuleService)(nil)

type statusDecode struct {
	Status *influxdb.Status `json:"status"`
}

// NotificationRuleBackend is all services and associated parameters required to construct
// the NotificationRuleBackendHandler.
type NotificationRuleBackend struct {
	influxdb.HTTPErrorHandler
	log *zap.Logger

	NotificationRuleStore       influxdb.NotificationRuleStore
	NotificationEndpointService influxdb.NotificationEndpointService
	UserResourceMappingService  influxdb.UserResourceMappingService
	LabelService                influxdb.LabelService
	UserService                 influxdb.UserService
	OrganizationService         influxdb.OrganizationService
	TaskService                 influxdb.TaskService
}

// NewNotificationRuleBackend returns a new instance of NotificationRuleBackend.
func NewNotificationRuleBackend(log *zap.Logger, b *APIBackend) *NotificationRuleBackend {
	return &NotificationRuleBackend{
		HTTPErrorHandler: b.HTTPErrorHandler,
		log:              log,

		NotificationRuleStore:       b.NotificationRuleStore,
		NotificationEndpointService: b.NotificationEndpointService,
		UserResourceMappingService:  b.UserResourceMappingService,
		LabelService:                b.LabelService,
		UserService:                 b.UserService,
		OrganizationService:         b.OrganizationService,
		TaskService:                 b.TaskService,
	}
}

// NotificationRuleHandler is the handler for the notification rule service
type NotificationRuleHandler struct {
	*httprouter.Router
	influxdb.HTTPErrorHandler
	log *zap.Logger

	NotificationRuleStore       influxdb.NotificationRuleStore
	NotificationEndpointService influxdb.NotificationEndpointService
	UserResourceMappingService  influxdb.UserResourceMappingService
	LabelService                influxdb.LabelService
	UserService                 influxdb.UserService
	OrganizationService         influxdb.OrganizationService
	TaskService                 influxdb.TaskService
}

const (
	prefixNotificationRules          = "/api/v2/notificationRules"
	notificationRulesIDPath          = "/api/v2/notificationRules/:id"
	notificationRulesIDQueryPath     = "/api/v2/notificationRules/:id/query"
	notificationRulesIDMembersPath   = "/api/v2/notificationRules/:id/members"
	notificationRulesIDMembersIDPath = "/api/v2/notificationRules/:id/members/:userID"
	notificationRulesIDOwnersPath    = "/api/v2/notificationRules/:id/owners"
	notificationRulesIDOwnersIDPath  = "/api/v2/notificationRules/:id/owners/:userID"
	notificationRulesIDLabelsPath    = "/api/v2/notificationRules/:id/labels"
	notificationRulesIDLabelsIDPath  = "/api/v2/notificationRules/:id/labels/:lid"
)

// NewNotificationRuleHandler returns a new instance of NotificationRuleHandler.
func NewNotificationRuleHandler(log *zap.Logger, b *NotificationRuleBackend) *NotificationRuleHandler {
	h := &NotificationRuleHandler{
		Router:           NewRouter(b.HTTPErrorHandler),
		HTTPErrorHandler: b.HTTPErrorHandler,
		log:              log,

		NotificationRuleStore:       b.NotificationRuleStore,
		NotificationEndpointService: b.NotificationEndpointService,
		UserResourceMappingService:  b.UserResourceMappingService,
		LabelService:                b.LabelService,
		UserService:                 b.UserService,
		OrganizationService:         b.OrganizationService,
		TaskService:                 b.TaskService,
	}
	h.HandlerFunc("POST", prefixNotificationRules, h.handlePostNotificationRule)
	h.HandlerFunc("GET", prefixNotificationRules, h.handleGetNotificationRules)
	h.HandlerFunc("GET", notificationRulesIDPath, h.handleGetNotificationRule)
	h.HandlerFunc("GET", notificationRulesIDQueryPath, h.handleGetNotificationRuleQuery)
	h.HandlerFunc("DELETE", notificationRulesIDPath, h.handleDeleteNotificationRule)
	h.HandlerFunc("PUT", notificationRulesIDPath, h.handlePutNotificationRule)
	h.HandlerFunc("PATCH", notificationRulesIDPath, h.handlePatchNotificationRule)

	memberBackend := MemberBackend{
		HTTPErrorHandler:           b.HTTPErrorHandler,
		log:                        b.log.With(zap.String("handler", "member")),
		ResourceType:               influxdb.NotificationRuleResourceType,
		UserType:                   influxdb.Member,
		UserResourceMappingService: b.UserResourceMappingService,
		UserService:                b.UserService,
	}
	h.HandlerFunc("POST", notificationRulesIDMembersPath, newPostMemberHandler(memberBackend))
	h.HandlerFunc("GET", notificationRulesIDMembersPath, newGetMembersHandler(memberBackend))
	h.HandlerFunc("DELETE", notificationRulesIDMembersIDPath, newDeleteMemberHandler(memberBackend))

	ownerBackend := MemberBackend{
		HTTPErrorHandler:           b.HTTPErrorHandler,
		log:                        b.log.With(zap.String("handler", "member")),
		ResourceType:               influxdb.NotificationRuleResourceType,
		UserType:                   influxdb.Owner,
		UserResourceMappingService: b.UserResourceMappingService,
		UserService:                b.UserService,
	}
	h.HandlerFunc("POST", notificationRulesIDOwnersPath, newPostMemberHandler(ownerBackend))
	h.HandlerFunc("GET", notificationRulesIDOwnersPath, newGetMembersHandler(ownerBackend))
	h.HandlerFunc("DELETE", notificationRulesIDOwnersIDPath, newDeleteMemberHandler(ownerBackend))

	labelBackend := &LabelBackend{
		HTTPErrorHandler: b.HTTPErrorHandler,
		log:              b.log.With(zap.String("handler", "label")),
		LabelService:     b.LabelService,
		ResourceType:     influxdb.TelegrafsResourceType,
	}
	h.HandlerFunc("GET", notificationRulesIDLabelsPath, newGetLabelsHandler(labelBackend))
	h.HandlerFunc("POST", notificationRulesIDLabelsPath, newPostLabelHandler(labelBackend))
	h.HandlerFunc("DELETE", notificationRulesIDLabelsIDPath, newDeleteLabelHandler(labelBackend))

	return h
}

type notificationRuleLinks struct {
	Self    string `json:"self"`
	Labels  string `json:"labels"`
	Members string `json:"members"`
	Owners  string `json:"owners"`
	Query   string `json:"query"`
}

type notificationRuleResponse struct {
	influxdb.NotificationRule
	Labels          []influxdb.Label      `json:"labels"`
	Links           notificationRuleLinks `json:"links"`
	Status          string                `json:"status"`
	LatestCompleted time.Time             `json:"latestCompleted,omitempty"`
	LatestScheduled time.Time             `json:"latestScheduled,omitempty"`
	LastRunStatus   string                `json:"LastRunStatus,omitempty"`
	LastRunError    string                `json:"LastRunError,omitempty"`
}

func (resp notificationRuleResponse) MarshalJSON() ([]byte, error) {
	b1, err := json.Marshal(resp.NotificationRule)
	if err != nil {
		return nil, err
	}

	b2, err := json.Marshal(struct {
		Labels          []influxdb.Label      `json:"labels"`
		Links           notificationRuleLinks `json:"links"`
		Status          string                `json:"status"`
		LatestCompleted time.Time             `json:"latestCompleted,omitempty"`
		LatestScheduled time.Time             `json:"latestScheduled,omitempty"`
		LastRunStatus   string                `json:"lastRunStatus,omitempty"`
		LastRunError    string                `json:"lastRunError,omitempty"`
	}{
		Links:           resp.Links,
		Labels:          resp.Labels,
		Status:          resp.Status,
		LatestCompleted: resp.LatestCompleted,
		LatestScheduled: resp.LatestScheduled,
		LastRunStatus:   resp.LastRunStatus,
		LastRunError:    resp.LastRunError,
	})
	if err != nil {
		return nil, err
	}

	return []byte(string(b1[:len(b1)-1]) + ", " + string(b2[1:])), nil
}

type notificationRulesResponse struct {
	NotificationRules []*notificationRuleResponse `json:"notificationRules"`
	Links             *influxdb.PagingLinks       `json:"links"`
}

func (h *NotificationRuleHandler) newNotificationRuleResponse(ctx context.Context, nr influxdb.NotificationRule, labels []*influxdb.Label) (*notificationRuleResponse, error) {
	// TODO(desa): this should be handled in the rule service and not exposed in http land, but is currently blocking the FE. https://github.com/influxdata/influxdb/issues/15259
	t, err := h.TaskService.FindTaskByID(ctx, nr.GetTaskID())
	if err != nil {
		return nil, err
	}

	nr.ClearPrivateData()
	res := &notificationRuleResponse{
		NotificationRule: nr,
		Links: notificationRuleLinks{
			Self:    fmt.Sprintf("/api/v2/notificationRules/%s", nr.GetID()),
			Labels:  fmt.Sprintf("/api/v2/notificationRules/%s/labels", nr.GetID()),
			Members: fmt.Sprintf("/api/v2/notificationRules/%s/members", nr.GetID()),
			Owners:  fmt.Sprintf("/api/v2/notificationRules/%s/owners", nr.GetID()),
			Query:   fmt.Sprintf("/api/v2/notificationRules/%s/query", nr.GetID()),
		},
		Labels:          []influxdb.Label{},
		Status:          t.Status,
		LatestCompleted: t.LatestCompleted,
		LatestScheduled: t.LatestScheduled,
		LastRunStatus:   t.LastRunStatus,
		LastRunError:    t.LastRunError,
	}

	for _, l := range labels {
		res.Labels = append(res.Labels, *l)
	}

	return res, nil
}

func (h *NotificationRuleHandler) newNotificationRulesResponse(ctx context.Context, nrs []influxdb.NotificationRule, labelService influxdb.LabelService, f influxdb.PagingFilter, opts influxdb.FindOptions) (*notificationRulesResponse, error) {
	resp := &notificationRulesResponse{
		NotificationRules: []*notificationRuleResponse{},
		Links:             newPagingLinks(prefixNotificationRules, opts, f, len(nrs)),
	}
	for _, nr := range nrs {
		labels, _ := labelService.FindResourceLabels(ctx, influxdb.LabelMappingFilter{ResourceID: nr.GetID()})
		res, err := h.newNotificationRuleResponse(ctx, nr, labels)
		if err != nil {
			continue
		}
		resp.NotificationRules = append(resp.NotificationRules, res)
	}
	return resp, nil
}

func decodeGetNotificationRuleRequest(ctx context.Context, r *http.Request) (i influxdb.ID, err error) {
	params := httprouter.ParamsFromContext(ctx)
	id := params.ByName("id")
	if id == "" {
		return i, &influxdb.Error{
			Code: influxdb.EInvalid,
			Msg:  "url missing id",
		}
	}

	if err := i.DecodeFromString(id); err != nil {
		return i, err
	}
	return i, nil
}

func (h *NotificationRuleHandler) handleGetNotificationRules(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	filter, opts, err := decodeNotificationRuleFilter(ctx, r)
	if err != nil {
		h.log.Debug("Failed to decode request", zap.Error(err))
		h.HandleHTTPError(ctx, err, w)
		return
	}
	nrs, _, err := h.NotificationRuleStore.FindNotificationRules(ctx, *filter, *opts)
	if err != nil {
		h.HandleHTTPError(ctx, err, w)
		return
	}
	h.log.Debug("Notification rules retrieved", zap.String("notificationRules", fmt.Sprint(nrs)))

	res, err := h.newNotificationRulesResponse(ctx, nrs, h.LabelService, filter, *opts)
	if err != nil {
		h.HandleHTTPError(ctx, err, w)
		return
	}

	if err := encodeResponse(ctx, w, http.StatusOK, res); err != nil {
		logEncodingError(h.log, r, err)
		return
	}
}

func (h *NotificationRuleHandler) handleGetNotificationRuleQuery(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id, err := decodeGetNotificationRuleRequest(ctx, r)
	if err != nil {
		h.HandleHTTPError(ctx, err, w)
		return
	}
	nr, err := h.NotificationRuleStore.FindNotificationRuleByID(ctx, id)
	if err != nil {
		h.HandleHTTPError(ctx, err, w)
		return
	}
	edp, err := h.NotificationEndpointService.FindNotificationEndpointByID(ctx, nr.GetEndpointID())
	if err != nil {
		h.HandleHTTPError(ctx, &influxdb.Error{
			Code: influxdb.EInternal,
			Op:   "http/handleGetNotificationRuleQuery",
			Err:  err,
		}, w)
		return
	}
	flux, err := nr.GenerateFlux(edp)
	if err != nil {
		h.HandleHTTPError(ctx, err, w)
		return
	}
	h.log.Debug("Notification rule query retrieved", zap.String("notificationRuleQuery", fmt.Sprint(flux)))
	if err := encodeResponse(ctx, w, http.StatusOK, newFluxResponse(flux)); err != nil {
		logEncodingError(h.log, r, err)
		return
	}
}

func (h *NotificationRuleHandler) handleGetNotificationRule(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id, err := decodeGetNotificationRuleRequest(ctx, r)
	if err != nil {
		h.HandleHTTPError(ctx, err, w)
		return
	}
	nr, err := h.NotificationRuleStore.FindNotificationRuleByID(ctx, id)
	if err != nil {
		h.HandleHTTPError(ctx, err, w)
		return
	}
	h.log.Debug("Notification rule retrieved", zap.String("notificationRule", fmt.Sprint(nr)))

	labels, err := h.LabelService.FindResourceLabels(ctx, influxdb.LabelMappingFilter{ResourceID: nr.GetID()})
	if err != nil {
		h.HandleHTTPError(ctx, err, w)
		return
	}

	res, err := h.newNotificationRuleResponse(ctx, nr, labels)
	if err != nil {
		h.HandleHTTPError(ctx, err, w)
		return
	}

	if err := encodeResponse(ctx, w, http.StatusOK, res); err != nil {
		logEncodingError(h.log, r, err)
		return
	}
}

func decodeNotificationRuleFilter(ctx context.Context, r *http.Request) (*influxdb.NotificationRuleFilter, *influxdb.FindOptions, error) {
	f := &influxdb.NotificationRuleFilter{}
	urm, err := decodeUserResourceMappingFilter(ctx, r, influxdb.NotificationRuleResourceType)
	if err == nil {
		f.UserResourceMappingFilter = *urm
	}

	opts, err := decodeFindOptions(r)
	if err != nil {
		return f, nil, err
	}

	q := r.URL.Query()
	if orgIDStr := q.Get("orgID"); orgIDStr != "" {
		orgID, err := influxdb.IDFromString(orgIDStr)
		if err != nil {
			return f, opts, &influxdb.Error{
				Code: influxdb.EInvalid,
				Msg:  "orgID is invalid",
				Err:  err,
			}
		}
		f.OrgID = orgID
	} else if orgNameStr := q.Get("org"); orgNameStr != "" {
		*f.Organization = orgNameStr
	}

	for _, tag := range q["tag"] {
		tp, err := influxdb.NewTag(tag)
		// ignore malformed tag pairs
		if err == nil {
			f.Tags = append(f.Tags, tp)
		}
	}

	return f, opts, err
}

func decodeUserResourceMappingFilter(ctx context.Context, r *http.Request, typ influxdb.ResourceType) (*influxdb.UserResourceMappingFilter, error) {
	q := r.URL.Query()
	f := &influxdb.UserResourceMappingFilter{
		ResourceType: typ,
	}
	if idStr := q.Get("resourceID"); idStr != "" {
		id, err := influxdb.IDFromString(idStr)
		if err != nil {
			return nil, err
		}
		f.ResourceID = *id
	}

	if idStr := q.Get("userID"); idStr != "" {
		id, err := influxdb.IDFromString(idStr)
		if err != nil {
			return nil, err
		}
		f.UserID = *id
	}
	return f, nil
}

type postNotificationRuleRequest struct {
	influxdb.NotificationRuleCreate
	Labels []string `json:"labels"`
}

func decodePostNotificationRuleRequest(ctx context.Context, r *http.Request) (postNotificationRuleRequest, error) {
	var pnrr postNotificationRuleRequest
	var sts statusDecode
	var dl decodeLabels

	buf := new(bytes.Buffer)
	_, err := buf.ReadFrom(r.Body)
	if err != nil {
		return pnrr, &influxdb.Error{
			Code: influxdb.EInvalid,
			Err:  err,
		}
	}
	defer r.Body.Close()

	nr, err := rule.UnmarshalJSON(buf.Bytes())
	if err != nil {
		return pnrr, &influxdb.Error{
			Code: influxdb.EInvalid,
			Err:  err,
		}
	}

	if err := json.Unmarshal(buf.Bytes(), &sts); err != nil {
		return pnrr, err
	}

	if err := json.Unmarshal(buf.Bytes(), &dl); err != nil {
		return pnrr, err
	}

	pnrr = postNotificationRuleRequest{
		NotificationRuleCreate: influxdb.NotificationRuleCreate{
			NotificationRule: nr,
			Status:           *sts.Status,
		},
		Labels: dl.Labels,
	}

	return pnrr, nil
}

func decodePutNotificationRuleRequest(ctx context.Context, r *http.Request) (influxdb.NotificationRuleCreate, error) {
	var nrc influxdb.NotificationRuleCreate
	var sts statusDecode

	buf := new(bytes.Buffer)
	_, err := buf.ReadFrom(r.Body)
	if err != nil {
		return nrc, &influxdb.Error{
			Code: influxdb.EInvalid,
			Err:  err,
		}
	}
	defer r.Body.Close()
	nr, err := rule.UnmarshalJSON(buf.Bytes())
	if err != nil {
		return nrc, &influxdb.Error{
			Code: influxdb.EInvalid,
			Err:  err,
		}
	}
	params := httprouter.ParamsFromContext(ctx)
	id := params.ByName("id")
	if id == "" {
		return nrc, &influxdb.Error{
			Code: influxdb.EInvalid,
			Msg:  "url missing id",
		}
	}
	i := new(influxdb.ID)
	if err := i.DecodeFromString(id); err != nil {
		return nrc, err
	}
	nr.SetID(*i)

	err = json.Unmarshal(buf.Bytes(), &sts)
	if err != nil {
		return nrc, err
	}

	nrc = influxdb.NotificationRuleCreate{
		NotificationRule: nr,
		Status:           *sts.Status,
	}

	return nrc, nil
}

type patchNotificationRuleRequest struct {
	influxdb.ID
	Update influxdb.NotificationRuleUpdate
}

func decodePatchNotificationRuleRequest(ctx context.Context, r *http.Request) (*patchNotificationRuleRequest, error) {
	req := &patchNotificationRuleRequest{}
	params := httprouter.ParamsFromContext(ctx)
	id := params.ByName("id")
	if id == "" {
		return nil, &influxdb.Error{
			Code: influxdb.EInvalid,
			Msg:  "url missing id",
		}
	}

	var i influxdb.ID
	if err := i.DecodeFromString(id); err != nil {
		return nil, err
	}
	req.ID = i

	upd := &influxdb.NotificationRuleUpdate{}
	if err := json.NewDecoder(r.Body).Decode(upd); err != nil {
		return nil, &influxdb.Error{
			Code: influxdb.EInvalid,
			Msg:  err.Error(),
		}
	}
	if err := upd.Valid(); err != nil {
		return nil, &influxdb.Error{
			Code: influxdb.EInvalid,
			Msg:  err.Error(),
		}
	}

	req.Update = *upd
	return req, nil
}

// handlePostNotificationRule is the HTTP handler for the POST /api/v2/notificationRules route.
func (h *NotificationRuleHandler) handlePostNotificationRule(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	nr, err := decodePostNotificationRuleRequest(ctx, r)
	if err != nil {
		h.log.Debug("Failed to decode request", zap.Error(err))
		h.HandleHTTPError(ctx, err, w)
		return
	}

	auth, err := pctx.GetAuthorizer(ctx)
	if err != nil {
		h.HandleHTTPError(ctx, err, w)
		return
	}

	if err := h.NotificationRuleStore.CreateNotificationRule(ctx, nr.NotificationRuleCreate, auth.GetUserID()); err != nil {
		h.HandleHTTPError(ctx, err, w)
		return
	}
	h.log.Debug("Notification rule created", zap.String("notificationRule", fmt.Sprint(nr)))

	labels := h.mapNewNotificationRuleLabels(ctx, nr.NotificationRuleCreate, nr.Labels)

	res, err := h.newNotificationRuleResponse(ctx, nr, labels)
	if err != nil {
		h.HandleHTTPError(ctx, err, w)
		return
	}

	if err := encodeResponse(ctx, w, http.StatusCreated, res); err != nil {
		logEncodingError(h.log, r, err)
		return
	}
}

func (h *NotificationRuleHandler) mapNewNotificationRuleLabels(ctx context.Context, nrc influxdb.NotificationRuleCreate, labels []string) []*influxdb.Label {
	var ls []*influxdb.Label
	for _, sid := range labels {
		var lid influxdb.ID
		err := lid.DecodeFromString(sid)

		if err != nil {
			continue
		}

		label, err := h.LabelService.FindLabelByID(ctx, lid)
		if err != nil {
			continue
		}

		mapping := influxdb.LabelMapping{
			LabelID:      label.ID,
			ResourceID:   nrc.GetID(),
			ResourceType: influxdb.NotificationRuleResourceType,
		}

		err = h.LabelService.CreateLabelMapping(ctx, &mapping)
		if err != nil {
			continue
		}

		ls = append(ls, label)
	}
	return ls
}

// handlePutNotificationRule is the HTTP handler for the PUT /api/v2/notificationRule route.
func (h *NotificationRuleHandler) handlePutNotificationRule(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	nrc, err := decodePutNotificationRuleRequest(ctx, r)
	if err != nil {
		h.log.Debug("Failed to decode request", zap.Error(err))
		h.HandleHTTPError(ctx, err, w)
		return
	}
	auth, err := pctx.GetAuthorizer(ctx)
	if err != nil {
		h.HandleHTTPError(ctx, err, w)
		return
	}

	nr, err := h.NotificationRuleStore.UpdateNotificationRule(ctx, nrc.GetID(), nrc, auth.GetUserID())
	if err != nil {
		h.HandleHTTPError(ctx, err, w)
		return
	}

	labels, err := h.LabelService.FindResourceLabels(ctx, influxdb.LabelMappingFilter{ResourceID: nr.GetID()})
	if err != nil {
		h.HandleHTTPError(ctx, err, w)
		return
	}
	h.log.Debug("Notification rule updated", zap.String("notificationRule", fmt.Sprint(nr)))

	res, err := h.newNotificationRuleResponse(ctx, nr, labels)
	if err != nil {
		h.HandleHTTPError(ctx, err, w)
		return
	}

	if err := encodeResponse(ctx, w, http.StatusOK, res); err != nil {
		logEncodingError(h.log, r, err)
		return
	}
}

// handlePatchNotificationRule is the HTTP handler for the PATCH /api/v2/notificationRule/:id route.
func (h *NotificationRuleHandler) handlePatchNotificationRule(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	req, err := decodePatchNotificationRuleRequest(ctx, r)
	if err != nil {
		h.log.Debug("Failed to decode request", zap.Error(err))
		h.HandleHTTPError(ctx, err, w)
		return
	}

	nr, err := h.NotificationRuleStore.PatchNotificationRule(ctx, req.ID, req.Update)
	if err != nil {
		h.HandleHTTPError(ctx, err, w)
		return
	}

	labels, err := h.LabelService.FindResourceLabels(ctx, influxdb.LabelMappingFilter{ResourceID: nr.GetID()})
	if err != nil {
		h.HandleHTTPError(ctx, err, w)
		return
	}
	h.log.Debug("Notification rule patch", zap.String("notificationRule", fmt.Sprint(nr)))

	res, err := h.newNotificationRuleResponse(ctx, nr, labels)
	if err != nil {
		h.HandleHTTPError(ctx, err, w)
		return
	}

	if err := encodeResponse(ctx, w, http.StatusOK, res); err != nil {
		logEncodingError(h.log, r, err)
		return
	}
}

func (h *NotificationRuleHandler) handleDeleteNotificationRule(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	i, err := decodeGetNotificationRuleRequest(ctx, r)
	if err != nil {
		h.HandleHTTPError(ctx, err, w)
		return
	}

	if err = h.NotificationRuleStore.DeleteNotificationRule(ctx, i); err != nil {
		h.HandleHTTPError(ctx, err, w)
		return
	}
	h.log.Debug("Notification rule deleted", zap.String("notificationRuleID", fmt.Sprint(i)))

	w.WriteHeader(http.StatusNoContent)
}

/*
type NotificationRuleStore interface {
	// UserResourceMappingService must be part of all NotificationRuleStore service,
	// for create, search, delete.
	UserResourceMappingService
	// OrganizationService is needed for search filter
	OrganizationService

	// FindNotificationRuleByID returns a single notification rule by ID.
	FindNotificationRuleByID(ctx context.Context, id ID) (NotificationRule, error)

	// FindNotificationRules returns a list of notification rules that match filter and the total count of matching notification rules.
	// Additional options provide pagination & sorting.
	FindNotificationRules(ctx context.Context, filter NotificationRuleFilter, opt ...FindOptions) ([]NotificationRule, int, error)

	// CreateNotificationRule creates a new notification rule and sets b.ID with the new identifier.
	CreateNotificationRule(ctx context.Context, nr NotificationRuleCreate, userID ID) error

	// UpdateNotificationRuleUpdateNotificationRule updates a single notification rule.
	// Returns the new notification rule after update.
	UpdateNotificationRule(ctx context.Context, id ID, nr NotificationRuleCreate, userID ID) (NotificationRule, error)

	// PatchNotificationRule updates a single  notification rule with changeset.
	// Returns the new notification rule state after update.
	PatchNotificationRule(ctx context.Context, id ID, upd NotificationRuleUpdate) (NotificationRule, error)

	// DeleteNotificationRule removes a notification rule by ID.
	DeleteNotificationRule(ctx context.Context, id ID) error
}
*/

// type NotificationRuleLinks struct {
// 	Self    string `json:"self"`
// 	Labels  string `json:"labels"`
// 	Members string `json:"members"`
// 	Owners  string `json:"owners"`
// 	Query   string `json:"query"`
// }

type NotificationRuleService struct {
	Client *httpc.Client
	*UserResourceMappingService
	*OrganizationService
}

func NewNotificationRuleService(client *httpc.Client) *NotificationRuleService {
	return &NotificationRuleService{
		Client: client,
		UserResourceMappingService: &UserResourceMappingService{
			Client: client,
		},
		OrganizationService: &OrganizationService{
			Client: client,
		},
	}
}

/*
type: "pagerduty"
every: "10m"
offset: "0s"
url: ""
orgID: "7554d5ad97b0cd33"
name: "just a little rule"
activeStatus: "active"
status: "active"
endpointID: "04aa72fef0302000"
tagRules: []
labels: []
statusRules: [{currentLevel: "CRIT", period: "1h", count: 1}]
description: ""
messageTemplate: "Notification Rule: ${ r._notification_rule_name } triggered by check: ${ r._check_name }: ${ r._message }"
*/
/*
{
"endpointID": "string",
"orgID": "string",
"status": "active",
"name": "string",
"sleepUntil": "string",
"every": "string",
"offset": "string",
"runbookLink": "string",
"limitEvery": 0,
"limit": 0,
"tagRules": [
{}
],
"description": "string",
"statusRules": [
{}
],
"labels": [
{}
],
"type": "PostNotificationRule",
"channel": "string",
"messageTemplate": "string"
}
*/

// type NotificationRuleCreate struct {
// 	EndpointID  string `json:"endpointID"`
// 	OrgID       string `json:"orgID"`
// 	Status      string `json:"status"`
// 	Name        string `json:"name"`
// 	SleepUntil  string `json:"sleepUntil"`
// 	Every       string `json:"every"`
// 	Offset      string `json:"offset"`
// 	RunbookLink string `json:"runbookLink"`
// 	LimitEvery  int    `json:"limitEvery"`
// 	Limit       int    `json:"limit"`
// 	// TagRules    []struct {
// 	// 	Key      string `json:"key"`
// 	// 	Value    string `json:"value"`
// 	// 	Operator string `json:"operator"`
// 	// } `json:"tagRules"`
// 	Description     string                    `json:"description"`
// 	StatusRules     []notification.StatusRule `json:"statusRules"`
// 	Labels          []influxdb.Label          `json:"labels"`
// 	Type            string                    `json:"type"`
// 	Channel         string                    `json:"channel"`
// 	MessageTemplate string                    `json:"messageTemplate"`
// }

// type NotificationRule struct {
// 	ID          influxdb.ID `json:"id,omitempty"`
// 	Name        string      `json:"name"`
// 	Description string      `json:"description,omitempty"`
// 	EndpointID  influxdb.ID `json:"endpointID,omitempty"`
// 	OrgID       influxdb.ID `json:"orgID,omitempty"`
// 	OwnerID     influxdb.ID `json:"ownerID,omitempty"`
// 	TaskID      influxdb.ID `json:"taskID,omitempty"`
// 	// SleepUntil is an optional sleeptime to start a task.
// 	SleepUntil *time.Time             `json:"sleepUntil,omitempty"`
// 	Every      *notification.Duration `json:"every,omitempty"`
// 	// Offset represents a delay before execution.
// 	// It gets marshalled from a string duration, i.e.: "10s" is 10 seconds
// 	Offset      *notification.Duration    `json:"offset,omitempty"`
// 	RunbookLink string                    `json:"runbookLink"`
// 	TagRules    []notification.TagRule    `json:"tagRules,omitempty"`
// 	StatusRules []notification.StatusRule `json:"statusRules,omitempty"`
// 	*influxdb.Limit
// 	influxdb.CRUDLog

// 	// LatestCompleted time.Time `json:"latestCompleted"`
// 	// // LastRunStatus   string                    `json:"lastRunStatus"`
// 	// // LastRunError    string                    `json:"lastRunError"`
// 	// CreatedAt time.Time `json:"createdAt"`
// 	// UpdatedAt time.Time `json:"updatedAt"`
// 	// Status    string    `json:"status"`

// 	// Every  string `json:"every"`
// 	// Offset string `json:"offset"`
// 	// // RunbookLink     string                    `json:"runbookLink"`
// 	// // LimitEvery      int                       `json:"limitEvery"`
// 	// Limit int `json:"limit"`
// 	// // TagRules        []*influxdb.Tag           `json:"tagRules"`
// 	// StatusRules []notification.StatusRule `json:"statusRules"`
// 	// // Labels          []*influxdb.Label         `json:"labels"`
// 	// // Links           NotificationRuleLinks     `json:"links"`
// 	// Type string `json:"type"`
// 	// // Channel         string                    `json:"channel"`
// 	// // MessageTemplate string                    `json:"messageTemplate"`
// }

func (s *NotificationRuleService) CreateNotificationRule(ctx context.Context, nr influxdb.NotificationRuleCreate, userID influxdb.ID) error {
	var resp notificationRuleDecoder
	err := s.Client.
		PostJSON(nr, prefixNotificationRules).
		DecodeJSON(&resp).
		Do(ctx)

	return err
}

func (s *NotificationRuleService) FindNotificationRuleByID(ctx context.Context, id influxdb.ID) (influxdb.NotificationRule, error) {
	var resp notificationRuleDecoder
	err := s.Client.
		Get(getNotificationRulesIDPath(id)).
		DecodeJSON(&resp).
		Do(ctx)

	return resp.rule, err
}

// FindNotificationRules returns a list of notification rules that match filter and the total count of matching notification rules.
// Additional options provide pagination & sorting.
func (s *NotificationRuleService) FindNotificationRules(ctx context.Context, filter influxdb.NotificationRuleFilter, opt ...influxdb.FindOptions) ([]influxdb.NotificationRule, int, error) {
	return []influxdb.NotificationRule{}, 0, nil
}

// UpdateNotificationRule updates a single notification rule.
// Returns the new notification rule after update.
func (s *NotificationRuleService) UpdateNotificationRule(ctx context.Context, id influxdb.ID, nr influxdb.NotificationRuleCreate, userID influxdb.ID) (influxdb.NotificationRule, error) {
	return nil, nil
}

// PatchNotificationRule updates a single  notification rule with changeset.
// Returns the new notification rule state after update.
func (s *NotificationRuleService) PatchNotificationRule(ctx context.Context, id influxdb.ID, upd influxdb.NotificationRuleUpdate) (influxdb.NotificationRule, error) {
	return nil, nil
}

// DeleteNotificationRule removes a notification rule by ID.
func (s *NotificationRuleService) DeleteNotificationRule(ctx context.Context, id influxdb.ID) error {
	return nil
}

func getNotificationRulesIDPath(id influxdb.ID) string {
	return path.Join(prefixNotificationRules, id.String())
}

type notificationRuleDecoder struct {
	rule influxdb.NotificationRule
}
