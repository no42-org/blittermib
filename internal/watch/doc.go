// Package watch observes the user's MIB directory via fsnotify and
// debounces change events before triggering a recompile of affected
// modules. Module replacement in the store is transactional so the
// served view never reflects a partial state.
package watch
