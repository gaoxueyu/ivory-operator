/*
 Copyright 2021 - 2023 Highgo Solutions, Inc.
 Licensed under the Apache License, Version 2.0 (the "License");
 you may not use this file except in compliance with the License.
 You may obtain a copy of the License at

 http://www.apache.org/licenses/LICENSE-2.0

 Unless required by applicable law or agreed to in writing, software
 distributed under the License is distributed on an "AS IS" BASIS,
 WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 See the License for the specific language governing permissions and
 limitations under the License.
*/

package ivory

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"gotest.tools/v3/assert"

	"github.com/highgo/ivory-operator/internal/testing/cmp"
	"github.com/highgo/ivory-operator/pkg/apis/ivory-operator.highgo.com/v1beta1"
)

func TestWriteUsersInIvorySQL(t *testing.T) {
	ctx := context.Background()

	t.Run("Arguments", func(t *testing.T) {
		expected := errors.New("pass-through")
		exec := func(
			_ context.Context, stdin io.Reader, stdout, stderr io.Writer, command ...string,
		) error {
			assert.Assert(t, stdout != nil, "should capture stdout")
			assert.Assert(t, stderr != nil, "should capture stderr")
			return expected
		}

		assert.Equal(t, expected, WriteUsersInIvorySQL(ctx, exec, nil, nil))
	})

	t.Run("Empty", func(t *testing.T) {
		calls := 0
		exec := func(
			_ context.Context, stdin io.Reader, _, _ io.Writer, command ...string,
		) error {
			calls++

			b, err := io.ReadAll(stdin)
			assert.NilError(t, err)
			assert.Equal(t, string(b), strings.TrimSpace(`
SET search_path TO '';
CREATE TEMPORARY TABLE input (id serial, data json);
\copy input (data) from stdin with (format text)
\.
BEGIN;
SELECT pg_catalog.format('CREATE USER %I',
       pg_catalog.json_extract_path_text(input.data, 'username'))
  FROM input
 WHERE NOT EXISTS (
       SELECT 1 FROM pg_catalog.pg_roles
       WHERE rolname = pg_catalog.json_extract_path_text(input.data, 'username'))
 ORDER BY input.id
\gexec

SELECT pg_catalog.format('ALTER ROLE %I WITH %s PASSWORD %L',
       pg_catalog.json_extract_path_text(input.data, 'username'),
       pg_catalog.json_extract_path_text(input.data, 'options'),
       pg_catalog.json_extract_path_text(input.data, 'verifier'))
  FROM input ORDER BY input.id
\gexec

SELECT pg_catalog.format('GRANT ALL PRIVILEGES ON DATABASE %I TO %I',
       pg_catalog.json_array_elements_text(
       pg_catalog.json_extract_path(
       pg_catalog.json_strip_nulls(input.data), 'databases')),
       pg_catalog.json_extract_path_text(input.data, 'username'))
  FROM input ORDER BY input.id
\gexec
COMMIT;`))
			return nil
		}

		assert.NilError(t, WriteUsersInIvorySQL(ctx, exec, nil, nil))
		assert.Equal(t, calls, 1)

		assert.NilError(t, WriteUsersInIvorySQL(ctx, exec, []v1beta1.IvoryUserSpec{}, nil))
		assert.Equal(t, calls, 2)

		assert.NilError(t, WriteUsersInIvorySQL(ctx, exec, nil, map[string]string{}))
		assert.Equal(t, calls, 3)
	})

	t.Run("OptionalFields", func(t *testing.T) {
		calls := 0
		exec := func(
			_ context.Context, stdin io.Reader, _, _ io.Writer, command ...string,
		) error {
			calls++

			b, err := io.ReadAll(stdin)
			assert.NilError(t, err)
			assert.Assert(t, cmp.Contains(string(b), `
\copy input (data) from stdin with (format text)
{"databases":["db1"],"options":"","username":"user-no-options","verifier":""}
{"databases":null,"options":"some options here","username":"user-no-databases","verifier":""}
{"databases":null,"options":"","username":"user-with-verifier","verifier":"some$verifier"}
\.
`))
			return nil
		}

		assert.NilError(t, WriteUsersInIvorySQL(ctx, exec,
			[]v1beta1.IvoryUserSpec{
				{
					Name:      "user-no-options",
					Databases: []v1beta1.IvoryIdentifier{"db1"},
				},
				{
					Name:    "user-no-databases",
					Options: "some options here",
				},
				{
					Name: "user-with-verifier",
				},
			},
			map[string]string{
				"no-user":            "ignored",
				"user-with-verifier": "some$verifier",
			},
		))
		assert.Equal(t, calls, 1)
	})

	t.Run("IvorySuperuser", func(t *testing.T) {
		calls := 0
		exec := func(
			_ context.Context, stdin io.Reader, _, _ io.Writer, command ...string,
		) error {
			calls++

			b, err := io.ReadAll(stdin)
			assert.NilError(t, err)
			assert.Assert(t, cmp.Contains(string(b), `
\copy input (data) from stdin with (format text)
{"databases":["ivory"],"options":"LOGIN SUPERUSER","username":"ivory","verifier":"allowed"}
\.
`))
			return nil
		}

		assert.NilError(t, WriteUsersInIvorySQL(ctx, exec,
			[]v1beta1.IvoryUserSpec{
				{
					Name:      "ivory",
					Databases: []v1beta1.IvoryIdentifier{"all", "ignored"},
					Options:   "NOLOGIN CONNECTION LIMIT 0",
				},
			},
			map[string]string{
				"ivory": "allowed",
			},
		))
		assert.Equal(t, calls, 1)
	})
}
