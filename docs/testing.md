# Faking balloon submissions

Playbook for testing the balloon flow end-to-end without actually solving
contest problems. Useful when developing the frontend, the printer pipeline,
or the first-solve logic.

## 1. Find the IDs you need

In the MySQL CLI against the DOMjudge database:

```sql
USE domjudge;
SELECT cid, shortname, enabled FROM contest WHERE enabled=1;             -- @cid
SELECT cp.probid, p.name, cp.shortname FROM contestproblem cp
  JOIN problem p USING(probid) WHERE cp.cid=<your_cid>;                  -- @probid
SELECT teamid, name, enabled FROM team WHERE enabled=1 LIMIT 20;         -- @teamid
SELECT langid FROM language WHERE allow_submit=1;                        -- @langid (string)
```

## 2. Bring up both processes

- Terminal A: `just mockfeed` — listens on `:8090`.
- Terminal B: `just run` with `DOMJUDGE_EVENTFEED_URL=http://localhost:8090`
  in `.env`. The server log should show
  `event-feed override: http://localhost:8090`.

Why the mock feed: DOMjudge's real `/event-feed` is populated by its PHP
`EventLogService`. A raw SQL insert bypasses that, so the real feed never
emits an event and the hub never refreshes. The mock lets us inject a
trigger line on demand.

## 3. Insert a fake submission + judging + balloon
<!--5, 24, 13, 4 -->
```sql
SET @cid=5, @teamid=13, @probid=24, @langid='4';
INSERT INTO submission (cid,teamid,probid,langid,submittime,valid,externalid)
  VALUES (@cid,@teamid,@probid,@langid,UNIX_TIMESTAMP(),1,UUID());
SET @sid=LAST_INSERT_ID();
INSERT INTO judging
  (cid,submitid,starttime,endtime,result,valid,verified,seen,judge_completely,uuid)
  VALUES (@cid,@sid,UNIX_TIMESTAMP(),UNIX_TIMESTAMP(),'correct',1,1,0,0,UUID());
INSERT INTO balloon (cid,teamid,probid,submitid,done) VALUES (@cid,@teamid,@probid,@sid,0);
SELECT @sid AS submitid;
```

Notes:

- `judging.uuid` is `NOT NULL` with no default. `UUID()` is fine.
- DOMjudge has no DB trigger that creates the balloon row — that lives in
  `BalloonService::updateBalloons` in the PHP app — so the explicit
  `INSERT INTO balloon` is mandatory.
- `balloon` has a unique constraint on `(cid, teamid, probid)`. Same team
  + same problem twice will fail.

## 4. Kick the balloon server

```bash
just trigger
```

That POSTs to the mock feed's `/trigger`, which broadcasts one NDJSON event
line. The hub refetches `/balloons`, `/teams`, `/problems`, `/state` from
the real DOMjudge, diffs against its previous state, broadcasts `KIND_ADDED`
to subscribers, and dispatches the print goroutine.

## 5. Verify

- Frontend: the balloon shows up in the list.
- Printer: with `PRINTER_KIND=ipp`, CUPS-PDF drops a PDF into `~/cups-pdf/`
  (or whatever the queue's backend is).
- Logs: hub logs the new balloon; IPP submission errors (if any) surface
  as `print balloon <id>: …`.

## 6. Undo

```sql
SET @sid=729;
DELETE FROM balloon    WHERE submitid=@sid;
DELETE FROM judging    WHERE submitid=@sid;
DELETE FROM submission WHERE submitid=@sid;
```

Order matters (FK constraints): balloon → judging → submission.

## Tips

- **First-solve banner.** First solve is derived per problem from the
  earliest `balloon.time`, excluding teams whose group is in
  `NO_FIRST_SOLVE_GROUP_IDS`. To force a first-solve ticket for problem X,
  delete existing balloons for X first:
  ```sql
  DELETE FROM balloon WHERE probid=<X> AND cid=@cid;
  ```
  Then insert as above. The next eligible team gets the flag.

- **Testing MarkDone.** Click "done" in the UI, or
  `UPDATE balloon SET done=1 WHERE submitid=@sid;` followed by `just trigger`.

- **Multiple balloons for one team.** Different `@probid` is fine; same
  `(cid, teamid, probid)` tuple hits the unique constraint.

- **No printer attached.** Set `PRINTER_KIND=noop` (or leave unset) to log
  the ticket fields without rendering or submitting anything.

- **Standalone template render** for iterating on the Typst layout without
  the server in the loop:
  ```bash
  typst compile \
    --input datetime='23-05-2026 16:42' \
    --input ticket_id=719 \
    --input problem=C \
    --input color='#33aa55' \
    --input team_name='Eindhoven University of Technology' \
    --input team_id=18 \
    --input balloons='A,B,C,D,E,F,G,H' \
    --input delivered='A,B' \
    --input in_delivery='C,E' \
    --input first_solve=true \
    templates/balloon.typ /tmp/balloon.pdf
  ```
