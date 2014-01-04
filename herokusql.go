// TODO: Support setting regions
// TODO: Implement deprovision and plan change

package herokusql

import (
	"appengine"

	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"code.google.com/p/goauth2/appengine/serviceaccount"
	sqlsvc "code.google.com/p/google-api-go-client/sqladmin/v1beta3"
	"github.com/gorilla/mux"
)

const (
	password    = "f1937226ef79503baddc427190207e5d"
	projectName = "herokusql"
	authScope   = "https://www.googleapis.com/auth/sqlservice.admin"
	retryDelay  = time.Duration(time.Second * 2)
	retryCount  = 5
)

var tierMap = map[string]string{
	"trickle": "D0",
	"stream":  "D1",
	"river":   "D4",
	"deluge":  "D16",
	"torrent": "D32",

	// For testing
	"test": "D0",
}
var r = mux.NewRouter()

func init() {
	r.HandleFunc("/heroku/resources", provision).Methods("POST")
	r.HandleFunc("/heroku/resources/{id}", deprovision).Methods("DELETE")
	r.HandleFunc("/heroku/resources/{id}", changePlan).Methods("POST")
	http.Handle("/", r)
}

func checkAuth(r *http.Request) bool {
	s := strings.SplitN(r.Header.Get("Authorization"), " ", 2)
	if len(s) != 2 || s[0] != "Basic" {
		return false
	}
	b, err := base64.StdEncoding.DecodeString(s[1])
	if err != nil {
		return false
	}
	pair := strings.SplitN(string(b), ":", 2)
	if len(pair) != 2 {
		return false
	}

	if pair[1] != password {
		return false
	}
	return true
}

func service(c appengine.Context) (*sqlsvc.Service, error) {
	client, err := serviceaccount.NewClient(c, authScope)
	if err != nil {
		c.Errorf("svc acct: %v", err)
		return nil, err
	}
	sql, err := sqlsvc.New(client)
	if err != nil {
		c.Errorf("new svc: %v", err)
		return nil, err
	}
	return sql, nil
}

func provision(w http.ResponseWriter, r *http.Request) {
	if !checkAuth(r) {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	c := appengine.NewContext(r)

	herReq, err := request(c, r)
	if err != nil {
		http.Error(w, "", http.StatusBadRequest)
		return
	}
	instanceName := herReq.HerokuID[:strings.Index(herReq.HerokuID, "@")]
	tier, ok := tierMap[herReq.Plan]
	if !ok {
		c.Errorf("tier: %s", herReq.Plan)
		http.Error(w, "Invalid plan "+herReq.Plan, http.StatusBadRequest)
		return
	}

	sql, err := service(c)
	if err != nil {
		http.Error(w, "", http.StatusInternalServerError)
		return
	}
	if _, err := sql.Instances.Insert(projectName, &sqlsvc.DatabaseInstance{
		Project:  projectName,
		Instance: instanceName,
		Settings: &sqlsvc.Settings{
			ActivationPolicy:          "ON_DEMAND",
			AuthorizedGaeApplications: []string{appengine.AppID(c)},
			IpConfiguration: &sqlsvc.IpConfiguration{
				Enabled: true,
			},
			PricingPlan:     "PER_USE",
			ReplicationType: "ASYNCHRONOUS",
			Tier:            tier,
		},
	}).Do(); err != nil {
		// TODO: Handle conflict
		c.Errorf("insert: %v", err)
		http.Error(w, "Error", http.StatusInternalServerError)
		return
	}

	ip := getInstanceIP(c, sql, instanceName)
	if ip == "" {
		http.Error(w, "", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(herokuResponse{
		ID: instanceName,
		Config: herokuResponseConfig{
			InstanceURL: ip,
		},
		Message: "Provision successful!",
	})
}

func deprovision(w http.ResponseWriter, r *http.Request) {
	if !checkAuth(r) {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	c := appengine.NewContext(r)
	sql, err := service(c)
	if err != nil {
		http.Error(w, "", http.StatusInternalServerError)
		return
	}
	if _, err := sql.Instances.Delete(projectName, mux.Vars(r)["id"]).Do(); err != nil {
		c.Errorf("delete: %v", err)
		http.Error(w, "", http.StatusInternalServerError)
	}
}

func changePlan(w http.ResponseWriter, r *http.Request) {
	if !checkAuth(r) {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	c := appengine.NewContext(r)
	herReq, err := request(c, r)
	if err != nil {
		http.Error(w, "", http.StatusBadRequest)
		return
	}
	tier, ok := tierMap[herReq.Plan]
	if !ok {
		c.Errorf("tier: %s", herReq.Plan)
		http.Error(w, "Invalid plan "+herReq.Plan, http.StatusBadRequest)
		return
	}

	sql, err := service(c)
	if err != nil {
		http.Error(w, "", http.StatusInternalServerError)
		return
	}
	instanceName := mux.Vars(r)["id"]
	if _, err := sql.Instances.Update(projectName, instanceName, &sqlsvc.DatabaseInstance{
		Project:  projectName,
		Instance: instanceName,
		Settings: &sqlsvc.Settings{
			Tier: tier,
		},
	}).Do(); err != nil {
		c.Errorf("update: %v", err)
		http.Error(w, "", http.StatusInternalServerError)
		return
	}

	ip := getInstanceIP(c, sql, instanceName)
	if ip == "" {
		http.Error(w, "", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(herokuResponse{
		ID: instanceName,
		Config: herokuResponseConfig{
			InstanceURL: ip,
		},
		Message: "Plan change successful!",
	})
}

func getInstanceIP(c appengine.Context, sql *sqlsvc.Service, instanceName string) string {
	for i := 0; i < retryCount; i++ {
		inst, err := sql.Instances.Get(projectName, instanceName).Do()
		if err != nil {
			c.Errorf("get: %v", err)
			return ""
		}
		if inst.State != "RUNNABLE" {
			time.Sleep(retryDelay)
			continue
		}
		if inst.IpAddresses == nil {
			c.Errorf("runnable w/o IP")
			return ""
		}
		return inst.IpAddresses[0].IpAddress
	}
	c.Errorf("timeout")
	return ""
}

type herokuRequest struct {
	HerokuID    string `json:"heroku_id"`
	Plan        string `json:"plan"`
	CallbackURL string `json:"callback_url"`
}

func request(c appengine.Context, r *http.Request) (*herokuRequest, error) {
	defer r.Body.Close()
	var herReq herokuRequest
	if err := json.NewDecoder(r.Body).Decode(&herReq); err != nil {
		c.Errorf("decode: %v", err)
		return nil, err
	}
	return &herReq, nil
}

type herokuResponse struct {
	ID      string               `json:"id"`
	Config  herokuResponseConfig `json:"config"`
	Message string               `json:"message"`
}

type herokuResponseConfig struct {
	InstanceURL string `json:"GOOGLECLOUDSQL_URL"`
}
