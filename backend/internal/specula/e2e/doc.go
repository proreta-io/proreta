// Package e2e tests the real Specula executor against the real gateway handler and a real
// PostgreSQL: the two sides talk over HTTP through a link the test can cut, on a fake clock, with a
// fake prober and a temporary state directory. The tests are skipped without TEST_DATABASE_ADMIN_URL.
package e2e
