// Copyright 2017 Vector Creations Ltd
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package routing

import (
	"fmt"
	"net/http"

	appserviceAPI "github.com/matrix-org/dendrite/appservice/api"
	"github.com/matrix-org/dendrite/clientapi/auth/authtypes"
	"github.com/matrix-org/dendrite/clientapi/httputil"
	"github.com/matrix-org/dendrite/clientapi/jsonerror"
	"github.com/matrix-org/dendrite/common/config"
	roomserverAPI "github.com/matrix-org/dendrite/roomserver/api"
	"github.com/matrix-org/gomatrix"
	"github.com/matrix-org/gomatrixserverlib"
	"github.com/matrix-org/util"
)

// DirectoryRoom looks up a room alias
// nolint: gocyclo
func DirectoryRoom(
	req *http.Request,
	roomAlias string,
	federation *gomatrixserverlib.FederationClient,
	cfg *config.Dendrite,
	rsAPI roomserverAPI.RoomserverAliasAPI,
	asAPI appserviceAPI.AppServiceQueryAPI,
) util.JSONResponse {
	_, domain, err := gomatrixserverlib.SplitID('#', roomAlias)
	if err != nil {
		return util.JSONResponse{
			Code: http.StatusBadRequest,
			JSON: jsonerror.BadJSON("Room alias must be in the form '#localpart:domain'"),
		}
	}

	if domain == cfg.Matrix.ServerName {
		queryResp, err := getRoomIDForAlias(req, rsAPI, roomAlias)
		if err != nil {
			return httputil.LogThenError(req, err)
		}

		// List any roomIDs found associated with this alias
		if len(queryResp.RoomID) > 0 {
			return util.JSONResponse{
				Code: http.StatusOK,
				JSON: queryResp,
			}
		}

		// No rooms found locally, try our application services by making a call to
		// the appservice component
		aliasReq := appserviceAPI.RoomAliasExistsRequest{Alias: roomAlias}
		var aliasResp appserviceAPI.RoomAliasExistsResponse
		err = asAPI.RoomAliasExists(req.Context(), &aliasReq, &aliasResp)
		if err != nil {
			return httputil.LogThenError(req, err)
		}

		if aliasResp.AliasExists {
			// Query the roomserver API again. We should have the room now
			queryResp, err = getRoomIDForAlias(req, rsAPI, roomAlias)
			if err != nil {
				return httputil.LogThenError(req, err)
			}

			// List any roomIDs found associated with this alias
			if len(queryResp.RoomID) > 0 {
				return util.JSONResponse{
					Code: http.StatusOK,
					JSON: queryResp,
				}
			}
		}
	} else {
		// Query the federation for this room alias
		resp, err := federation.LookupRoomAlias(req.Context(), domain, roomAlias)
		if err != nil {
			switch err.(type) {
			case gomatrix.HTTPError:
			default:
				// TODO: Return 502 if the remote server errored.
				// TODO: Return 504 if the remote server timed out.
				return httputil.LogThenError(req, err)
			}
		}
		if len(resp.RoomID) > 0 {
			return util.JSONResponse{
				Code: http.StatusOK,
				JSON: resp,
			}
		}
	}

	return util.JSONResponse{
		Code: http.StatusNotFound,
		JSON: jsonerror.NotFound(
			fmt.Sprintf("Room alias %s not found", roomAlias),
		),
	}
}

// getRoomIDForAlias queries the roomserver API and returns a Directory Response
// on a successful query
func getRoomIDForAlias(
	req *http.Request,
	rsAPI roomserverAPI.RoomserverAliasAPI,
	roomAlias string,
) (resp gomatrixserverlib.RespDirectory, err error) {
	// Query the roomserver API to check if the alias exists locally
	queryReq := roomserverAPI.GetRoomIDForAliasRequest{Alias: roomAlias}
	var queryRes roomserverAPI.GetRoomIDForAliasResponse
	if err = rsAPI.GetRoomIDForAlias(req.Context(), &queryReq, &queryRes); err != nil {
		return
	}
	return gomatrixserverlib.RespDirectory{
		RoomID:  queryRes.RoomID,
		Servers: []gomatrixserverlib.ServerName{},
	}, nil
}

// SetLocalAlias implements PUT /directory/room/{roomAlias}
// TODO: Check if the user has the power level to set an alias
func SetLocalAlias(
	req *http.Request,
	device *authtypes.Device,
	alias string,
	cfg *config.Dendrite,
	aliasAPI roomserverAPI.RoomserverAliasAPI,
) util.JSONResponse {
	_, domain, err := gomatrixserverlib.SplitID('#', alias)
	if err != nil {
		return util.JSONResponse{
			Code: http.StatusBadRequest,
			JSON: jsonerror.BadJSON("Room alias must be in the form '#localpart:domain'"),
		}
	}

	if domain != cfg.Matrix.ServerName {
		return util.JSONResponse{
			Code: http.StatusForbidden,
			JSON: jsonerror.Forbidden("Alias must be on local homeserver"),
		}
	}

	var r struct {
		RoomID string `json:"room_id"`
	}
	if resErr := httputil.UnmarshalJSONRequest(req, &r); resErr != nil {
		return *resErr
	}

	queryReq := roomserverAPI.SetRoomAliasRequest{
		UserID: device.UserID,
		RoomID: r.RoomID,
		Alias:  alias,
	}
	var queryRes roomserverAPI.SetRoomAliasResponse
	if err := aliasAPI.SetRoomAlias(req.Context(), &queryReq, &queryRes); err != nil {
		return httputil.LogThenError(req, err)
	}

	if queryRes.AliasExists {
		return util.JSONResponse{
			Code: http.StatusConflict,
			JSON: jsonerror.Unknown("The alias " + alias + " already exists."),
		}
	}

	return util.JSONResponse{
		Code: http.StatusOK,
		JSON: struct{}{},
	}
}

// RemoveLocalAlias implements DELETE /directory/room/{roomAlias}
// TODO: Check if the user has the power level to remove an alias
func RemoveLocalAlias(
	req *http.Request,
	device *authtypes.Device,
	alias string,
	aliasAPI roomserverAPI.RoomserverAliasAPI,
) util.JSONResponse {
	queryReq := roomserverAPI.RemoveRoomAliasRequest{
		Alias:  alias,
		UserID: device.UserID,
	}
	var queryRes roomserverAPI.RemoveRoomAliasResponse
	if err := aliasAPI.RemoveRoomAlias(req.Context(), &queryReq, &queryRes); err != nil {
		return httputil.LogThenError(req, err)
	}

	return util.JSONResponse{
		Code: http.StatusOK,
		JSON: struct{}{},
	}
}
