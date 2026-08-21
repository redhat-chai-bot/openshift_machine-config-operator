package build

import (
	"sync"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Provides an easy way to ensure that any given object(s) are unique. In this
// case, we determine uniqueness by getting the object name and UID.
// All public methods are safe for concurrent use.
type uniqueObjects struct {
	mu      sync.Mutex
	objects map[uniqueObjectKey]metav1.Object
}

type uniqueObjectKey struct {
	Name string
	UID  string
}

func newUniqueObjects() *uniqueObjects {
	return &uniqueObjects{
		objects: map[uniqueObjectKey]metav1.Object{},
	}
}

// Returns a copy of the underlying map.
func (u *uniqueObjects) Map() map[uniqueObjectKey]metav1.Object {
	u.mu.Lock()
	defer u.mu.Unlock()

	out := make(map[uniqueObjectKey]metav1.Object, len(u.objects))
	for key, val := range u.objects {
		out[key] = val
	}
	return out
}

// Inserts a single object.
func (u *uniqueObjects) Insert(obj metav1.Object) {
	u.mu.Lock()
	defer u.mu.Unlock()

	key := u.computeKey(obj)
	if _, exists := u.objects[key]; !exists {
		u.objects[key] = obj
	}
}

// Inserts multiple objects.
func (u *uniqueObjects) InsertAll(objs []metav1.Object) {
	u.mu.Lock()
	defer u.mu.Unlock()

	for _, obj := range objs {
		key := u.computeKey(obj)
		if _, exists := u.objects[key]; !exists {
			u.objects[key] = obj
		}
	}
}

// Determines if the object already exists.
func (u *uniqueObjects) Has(obj metav1.Object) bool {
	u.mu.Lock()
	defer u.mu.Unlock()

	key := u.computeKey(obj)
	_, ok := u.objects[key]
	return ok
}

// Retrieves an object given a key, if found.
func (u *uniqueObjects) GetByKey(k uniqueObjectKey) (metav1.Object, bool) {
	u.mu.Lock()
	defer u.mu.Unlock()

	obj, ok := u.objects[k]
	return obj, ok
}

// Allows querying by key.
func (u *uniqueObjects) HasKey(k uniqueObjectKey) bool {
	u.mu.Lock()
	defer u.mu.Unlock()

	_, ok := u.objects[k]
	return ok
}

// Returns all keys.
func (u *uniqueObjects) Keys() []uniqueObjectKey {
	u.mu.Lock()
	defer u.mu.Unlock()

	out := make([]uniqueObjectKey, 0, len(u.objects))
	for key := range u.objects {
		out = append(out, key)
	}
	return out
}

// Computes the key for the object. Caller must hold u.mu.
func (u *uniqueObjects) computeKey(obj metav1.Object) uniqueObjectKey {
	return uniqueObjectKey{
		Name: obj.GetName(),
		UID:  string(obj.GetUID()),
	}
}

// Gets the length of the underlying map.
func (u *uniqueObjects) Len() int {
	u.mu.Lock()
	defer u.mu.Unlock()

	return len(u.objects)
}

// Returns an unsorted slice of the items in the map.
func (u *uniqueObjects) UnsortedSlice() []metav1.Object {
	u.mu.Lock()
	defer u.mu.Unlock()

	out := make([]metav1.Object, 0, len(u.objects))
	for _, obj := range u.objects {
		out = append(out, obj)
	}
	return out
}
