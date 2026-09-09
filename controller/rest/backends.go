package rest

import (
	"errors"
	"net/http"

	"github.com/pasarguard/node/common"
	"github.com/pasarguard/node/controller"
)

func (s *Service) AddBackend(w http.ResponseWriter, r *http.Request) {
	s.LockControl()
	defer s.UnlockControl()

	data := &common.Backend{}
	if err := common.ReadProtoBody(r.Body, data); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	if err := s.AttachBackend(r.Context(), data); err != nil {
		if errors.Is(err, controller.ErrBackendTypeNotShareable) {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}

	common.SendProtoResponse(w, &common.Empty{})
}

func (s *Service) RemoveBackend(w http.ResponseWriter, r *http.Request) {
	s.LockControl()
	defer s.UnlockControl()

	data := &common.RemoveBackendRequest{}
	if err := common.ReadProtoBody(r.Body, data); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	if err := s.DetachBackend(data.GetType()); err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}

	common.SendProtoResponse(w, &common.Empty{})
}

func (s *Service) ListBackends(w http.ResponseWriter, _ *http.Request) {
	common.SendProtoResponse(w, &common.BackendList{Types: s.BackendTypes()})
}
