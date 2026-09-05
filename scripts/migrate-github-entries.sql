-- issue #561: convert each legacy self-describing "github" destinations
-- entry on a missions row into a saved destinations table row (kind
-- 'github') plus an id-referencing entry, matching what api/missions.go's
-- create handler now writes directly. Idempotent: a rerun finds no more
-- self-describing entries and does nothing. Run with:
--   psql "$DATABASE_URL" -f scripts/migrate-github-entries.sql

DO $$
DECLARE
    m RECORD;
    elem jsonb;
    new_elems jsonb;
    idx int;
    connector_id text;
    mode text;
    branch_pattern text;
    commit_style text;
    create_if_missing boolean;
    dest_name text;
    dest_id uuid;
    cfg jsonb;
BEGIN
    FOR m IN
        SELECT id, destinations, sources
        FROM missions
        WHERE destinations @> '[{"destination": "github"}]'
    LOOP
        new_elems := '[]'::jsonb;
        FOR idx IN 0 .. jsonb_array_length(m.destinations) - 1 LOOP
            elem := m.destinations -> idx;
            IF elem ->> 'destination' <> 'github' THEN
                new_elems := new_elems || jsonb_build_array(elem);
                CONTINUE;
            END IF;

            connector_id := elem ->> 'connector_id';
            IF connector_id IS NULL OR connector_id = '' THEN
                -- Fall back to the mission's own github source connector_id.
                SELECT src.value ->> 'connector_id' INTO connector_id
                FROM jsonb_array_elements(coalesce(m.sources, '[]'::jsonb)) AS src(value)
                WHERE src.value ->> 'source' = 'github'
                LIMIT 1;
            END IF;
            IF connector_id IS NULL OR connector_id = '' THEN
                RAISE NOTICE 'mission %: github destination entry has no connector_id (own or clone-source), dropping', m.id;
                CONTINUE;
            END IF;

            mode := coalesce(nullif(elem ->> 'mode', ''), 'push');
            branch_pattern := elem ->> 'branch_pattern';
            commit_style := elem ->> 'commit_style';
            create_if_missing := coalesce((elem ->> 'create_if_missing')::boolean, false);

            dest_name := 'github-' || mode || '-' || left(connector_id, 8);

            cfg := jsonb_build_object('connector_id', connector_id, 'mode', mode);
            IF branch_pattern IS NOT NULL AND branch_pattern <> '' THEN
                cfg := cfg || jsonb_build_object('branch_pattern', branch_pattern);
            END IF;
            IF commit_style IS NOT NULL AND commit_style <> '' THEN
                cfg := cfg || jsonb_build_object('commit_style', commit_style);
            END IF;
            IF create_if_missing THEN
                cfg := cfg || jsonb_build_object('create_if_missing', true);
            END IF;

            SELECT id INTO dest_id FROM destinations WHERE name = dest_name;
            IF dest_id IS NULL THEN
                INSERT INTO destinations (name, kind, config, credential_ref, enabled)
                VALUES (dest_name, 'github', cfg, '', true)
                RETURNING id INTO dest_id;
            END IF;

            new_elems := new_elems || jsonb_build_array(
                jsonb_strip_nulls(jsonb_build_object(
                    'destination_id', dest_id::text,
                    'repo_url', nullif(elem ->> 'repo_url', ''),
                    'delivered_at', nullif(elem ->> 'delivered_at', ''),
                    'error', nullif(elem ->> 'error', '')
                ))
            );
        END LOOP;

        UPDATE missions SET destinations = new_elems WHERE id = m.id;
    END LOOP;
END $$;
