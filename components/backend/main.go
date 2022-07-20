package main

import (
	"encoding/json"
	"flag"
	"log"
	"net/http"
	"path/filepath"

	"github.com/gorilla/mux"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/util/homedir"

	subscription "github.com/vladislavpaskar/hackathon2022/components/backend/clients"

	eventingv1alpha1 "github.com/kyma-project/kyma/components/eventing-controller/api/v1alpha1"
)

//var Functions []xyz.Function //crud
//logs //get
//cloudevents //get

var subscriptionClient subscription.Client

type SubscriptionData struct {
	Sink  string   `json:"sink"`
	Types []string `json:"types"`
}

func main() {
	var kubeconfig *string
	if home := homedir.HomeDir(); home != "" && false {
		kubeconfig = flag.String("kubeconfig", filepath.Join(home, ".kube", "config"), "(optional) absolute path to the kubeconfig file")
	} else {
		kubeconfig = flag.String("kubeconfig", "/Users/I549741/Downloads/kubeconfig.yaml", "absolute path to the kubeconfig file")
	}

	flag.Parse()

	k8sConfig, err := clientcmd.BuildConfigFromFlags("", *kubeconfig)
	if err != nil {
		log.Fatal(err)
	}

	// Create dynamic client (k8s)
	dynamicClient := dynamic.NewForConfigOrDie(k8sConfig)

	// setup clients
	subscriptionClient = subscription.NewClient(dynamicClient)

	handleRequests()

}

func handleRequests() {
	r := mux.NewRouter().StrictSlash(true)

	r.Use(commonMiddleware)

	r.HandleFunc("/{ns}/subs/{name}", postSub).Methods("POST")
	r.HandleFunc("/subs", getAllSubs).Methods("GET")
	r.HandleFunc("/{ns}/subs/{name}", getSub).Methods("GET")
	r.HandleFunc("/{ns}/subs/{name}", putSub).Methods("PUT")
	r.HandleFunc("/{ns}/subs/{name}", delSub).Methods("DELETE")

	r.HandleFunc("/{ns}/funcs/{name}", postFuncs).Methods("POST")
	r.HandleFunc("/{ns}/funcs/{name}", getFuncs).Methods("GET")
	r.HandleFunc("/{ns}/funcs/{name}", putFuncs).Methods("PUT")
	r.HandleFunc("/{ns}/funcs/{name}", delFuncs).Methods("DELETE")

	log.Fatal(http.ListenAndServe(":8000", r))
	log.Printf("Server listening on port 8000 ...")
}

func commonMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		next.ServeHTTP(w, r)
	})
}

func getAllSubs(w http.ResponseWriter, r *http.Request) {
	subsUnstructured, err := subscriptionClient.ListJson("tunas-testing")
	if err != nil {
		log.Printf("%s %s failed: %v", r.Method, r.RequestURI, err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	// Convert response to bytes
	subsBytes, err := subsUnstructured.MarshalJSON()
	if err != nil {
		log.Printf("%s %s failed to marchal json: %v", r.Method, r.RequestURI, err)
	}

	// Return response to user
	_, err = w.Write(subsBytes)
	if err != nil {
		log.Printf("%s %s failed to write response: %v", r.Method, r.RequestURI, err)
	}
}

func postSub(w http.ResponseWriter, r *http.Request) {
	// Fetch data from URI
	namespace := mux.Vars(r)["ns"]
	name := mux.Vars(r)["name"]

	// Fetch data from request body
	var newSubData SubscriptionData
	err := json.NewDecoder(r.Body).Decode(&newSubData)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	// Initialize a subscription object
	newSub := &eventingv1alpha1.Subscription{
		Spec: eventingv1alpha1.SubscriptionSpec{
			Sink:   newSubData.Sink,
			Filter: &eventingv1alpha1.BEBFilters{},
		},
	}
	newSub.Kind = "Subscription"
	newSub.APIVersion = "eventing.kyma-project.io/v1alpha1"
	newSub.Name = name
	newSub.Namespace = namespace

	for _, eventType := range newSubData.Types {
		eventFilter := &eventingv1alpha1.BEBFilter{
			EventSource: &eventingv1alpha1.Filter{
				Property: "source",
				Type:     "exact",
				Value:    "",
			},
			EventType: &eventingv1alpha1.Filter{
				Property: "type",
				Type:     "exact",
				Value:    eventType,
			},
		}

		newSub.Spec.Filter.Filters = append(newSub.Spec.Filter.Filters, eventFilter)
	}

	// Create subscription on the k8s cluster
	_, err = subscriptionClient.CreateSubscription(*newSub)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	w.WriteHeader(http.StatusCreated)
}

func getSub(w http.ResponseWriter, r *http.Request) {

}

func putSub(w http.ResponseWriter, r *http.Request) {

}

func delSub(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	if vars["ns"] == "" {
		vars["ns"] = "default"
	}
	log.Printf("request to delete subscription %v in namespace %v", vars["name"], vars["ns"])
	//todo
}
func postFuncs(w http.ResponseWriter, r *http.Request) {

}
func getFuncs(w http.ResponseWriter, r *http.Request) {

}
func putFuncs(w http.ResponseWriter, r *http.Request) {

}
func delFuncs(w http.ResponseWriter, r *http.Request) {

}
