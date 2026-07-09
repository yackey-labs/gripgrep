// Package walk implements a parallel, gitignore-aware directory
// traversal: rg's "ignore" crate equivalent. It is designed around a
// work-stealing model (per-worker LIFO deque, unit of work = one
// directory, quiescence via an active-worker count) with an immutable,
// per-directory ignore-matcher stack built from package glob so a
// .gitignore is parsed once and shared by reference with every
// descendant directory.
package walk
