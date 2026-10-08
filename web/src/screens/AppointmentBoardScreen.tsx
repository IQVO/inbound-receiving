import { useState } from "react";
import { Link } from "react-router-dom";
import { Card } from "@warehouse/ui-kit";
import {
  bookAppointment,
  cancelAppointment,
  checkInAppointment,
  listAllAppointments,
  listDocks,
  toApiError,
} from "../api";
import type { ApiError } from "../api";
import { Form, FormRow, InlineError, InlineSuccess, SelectField, SubmitButton, TextField } from "../components/formkit";
import { PageHeader, Stack, StatePill } from "../components/layout";
import { EmptyNote, LoadingNote, ProblemAlert } from "../components/ProblemAlert";
import { useRequest } from "../hooks/useRequest";
import {
  MAX_WINDOW_MS,
  dayRange,
  doorCodeProblem,
  parseAsnNumbers,
  todayUtc,
  utcInputToIso,
  utcTime,
} from "../lib";
import type { Appointment, DockList } from "../types";

/**
 * The dock-appointment board: one column per inbound dock door for the chosen
 * UTC day (GET /appointments for the day's [from, to) window, GET /docks for
 * the doors), with check-in and cancel on each Booked appointment and a form
 * to book a new window. The day and every time on this screen are UTC.
 */
export function AppointmentBoardScreen() {
  const [day, setDay] = useState(todayUtc);
  const range = dayRange(day);
  const docks = useRequest(listDocks, "docks");
  const appointments = useRequest(
    range ? () => listAllAppointments({ from: range.from, to: range.to }) : null,
    `appointments|${day}`,
  );
  const [actionProblem, setActionProblem] = useState<ApiError | null>(null);
  const [busy, setBusy] = useState<string | null>(null);

  const act = async (id: string, run: () => Promise<unknown>) => {
    setActionProblem(null);
    setBusy(id);
    try {
      await run();
      appointments.reload();
    } catch (err) {
      setActionProblem(toApiError(err));
    } finally {
      setBusy(null);
    }
  };

  const dockList: DockList | null = docks.status === "success" ? docks.data : null;

  return (
    <Stack gap={5}>
      <PageHeader
        title="Dock appointments"
        subtitle="inbound-receiving · booked windows per dock door and day (all times UTC)"
      />
      <Card title="Day">
        <Form label="Choose day" onSubmit={() => {}}>
          <FormRow>
            <TextField label="Day (UTC)" type="date" value={day} onChange={setDay} />
          </FormRow>
          {!range && <InlineError message="Enter a valid date." />}
        </Form>
      </Card>
      {actionProblem && <ProblemAlert error={actionProblem} />}
      <Board
        day={day}
        docks={dockList}
        docksError={docks.status === "error" ? docks.error : null}
        appointments={appointments}
        busy={busy}
        onCheckIn={(a) => act(a.appointmentId, () => checkInAppointment(a.appointmentId))}
        onCancel={(a) => act(a.appointmentId, () => cancelAppointment(a.appointmentId))}
      />
      <BookCard day={day} docks={dockList} onBooked={appointments.reload} />
    </Stack>
  );
}

type BoardRequest = ReturnType<typeof useRequest<Appointment[]>>;

function Board({
  day,
  docks,
  docksError,
  appointments,
  busy,
  onCheckIn,
  onCancel,
}: {
  day: string;
  docks: DockList | null;
  docksError: ApiError | null;
  appointments: BoardRequest;
  busy: string | null;
  onCheckIn: (a: Appointment) => void;
  onCancel: (a: Appointment) => void;
}) {
  if (appointments.status === "idle") return null;
  if (appointments.status === "loading") return <LoadingNote what="appointments" />;
  if (appointments.status === "error") return <ProblemAlert error={appointments.error} />;

  const byDoor = new Map<string, Appointment[]>();
  for (const d of docks?.items ?? []) byDoor.set(d.doorCode, []);
  for (const a of appointments.data) byDoor.set(a.doorCode, [...(byDoor.get(a.doorCode) ?? []), a]);
  const doors = [...byDoor.keys()].sort();

  return (
    <Stack gap={4}>
      {docksError && <ProblemAlert error={docksError} lead="The dock door list could not be loaded." />}
      {doors.length === 0 ? (
        <EmptyNote>No appointments on {day} and no inbound dock doors are known.</EmptyNote>
      ) : (
        <div
          style={{
            display: "grid",
            gap: "var(--wh-space-4)",
            gridTemplateColumns: "repeat(auto-fill, minmax(280px, 1fr))",
          }}
        >
          {doors.map((door) => (
            <Card key={door} title={door}>
              <DoorColumn
                appointments={[...(byDoor.get(door) ?? [])].sort((a, b) => a.windowStart.localeCompare(b.windowStart))}
                busy={busy}
                onCheckIn={onCheckIn}
                onCancel={onCancel}
              />
            </Card>
          ))}
        </div>
      )}
    </Stack>
  );
}

function DoorColumn({
  appointments,
  busy,
  onCheckIn,
  onCancel,
}: {
  appointments: Appointment[];
  busy: string | null;
  onCheckIn: (a: Appointment) => void;
  onCancel: (a: Appointment) => void;
}) {
  if (appointments.length === 0) return <EmptyNote>Nothing booked.</EmptyNote>;
  return (
    <ul style={{ listStyle: "none", margin: 0, padding: 0, display: "flex", flexDirection: "column", gap: "var(--wh-space-3)" }}>
      {appointments.map((a) => {
        const when = `${utcTime(a.windowStart)}–${utcTime(a.windowEnd)}Z`;
        return (
          <li
            key={a.appointmentId}
            style={{
              border: "1px solid var(--wh-color-border)",
              borderRadius: "var(--wh-radius-md)",
              padding: "var(--wh-space-3)",
              display: "flex",
              flexDirection: "column",
              gap: 6,
              fontSize: "var(--wh-font-size-sm)",
            }}
          >
            <div style={{ display: "flex", justifyContent: "space-between", gap: 8 }}>
              <strong>{when}</strong>
              <StatePill state={a.state} />
            </div>
            <div>{a.carrier}</div>
            <div>
              ASNs:{" "}
              {a.asnNumbers.map((n, i) => (
                <span key={n}>
                  {i > 0 && ", "}
                  <Link to={`../asns/${encodeURIComponent(n)}`}>{n}</Link>
                </span>
              ))}
            </div>
            {a.state === "Booked" && (
              <div style={{ display: "flex", gap: "var(--wh-space-2)" }}>
                <SubmitButton
                  type="button"
                  disabled={busy === a.appointmentId}
                  ariaLabel={`Check in ${a.carrier} ${when}`}
                  onClick={() => onCheckIn(a)}
                >
                  Check in
                </SubmitButton>
                <SubmitButton
                  type="button"
                  secondary
                  disabled={busy === a.appointmentId}
                  ariaLabel={`Cancel ${a.carrier} ${when}`}
                  onClick={() => onCancel(a)}
                >
                  Cancel
                </SubmitButton>
              </div>
            )}
          </li>
        );
      })}
    </ul>
  );
}

/**
 * POST /appointments: books a door window for a carrier covering one or more
 * ASNs. The API owns the rules (window at most 4 hours, not in the past, no
 * overlap on the door, ASNs must exist); this form only catches what it can
 * word better than a 400.
 */
function BookCard({
  day,
  docks,
  onBooked,
}: {
  day: string;
  docks: DockList | null;
  onBooked: () => void;
}) {
  const [door, setDoor] = useState("");
  const [carrier, setCarrier] = useState("");
  const [start, setStart] = useState("");
  const [end, setEnd] = useState("");
  const [asns, setAsns] = useState("");
  const [invalid, setInvalid] = useState<string | null>(null);
  const [problem, setProblem] = useState<ApiError | null>(null);
  const [booked, setBooked] = useState<Appointment | null>(null);
  const [saving, setSaving] = useState(false);

  // With a kafka-fed door list the doors are a closed set; in permissive mode
  // (or when the list could not be loaded) any door code is accepted.
  const closedSet = docks !== null && docks.mode === "kafka" && docks.items.length > 0;

  const submit = async () => {
    setProblem(null);
    setBooked(null);
    const doorCode = door.trim();
    const doorWhy = doorCodeProblem(doorCode);
    if (doorWhy) return setInvalid(doorWhy);
    const carrierName = carrier.trim();
    if (carrierName === "") return setInvalid("Enter the carrier.");
    if (carrierName.length > 100) return setInvalid("A carrier name is at most 100 characters.");
    const windowStart = utcInputToIso(start);
    const windowEnd = utcInputToIso(end);
    if (!windowStart || !windowEnd) return setInvalid("Enter the window start and end (UTC).");
    const span = Date.parse(windowEnd) - Date.parse(windowStart);
    if (span <= 0) return setInvalid("The window must end after it starts.");
    if (span > MAX_WINDOW_MS) return setInvalid("A window lasts at most 4 hours.");
    const asnNumbers = [...new Set(parseAsnNumbers(asns))];
    if (asnNumbers.length === 0) return setInvalid("Enter at least one ASN number.");
    setInvalid(null);
    setSaving(true);
    try {
      const res = await bookAppointment({ doorCode, carrier: carrierName, windowStart, windowEnd, asnNumbers });
      setBooked(res.data);
      onBooked();
    } catch (err) {
      setProblem(toApiError(err));
    } finally {
      setSaving(false);
    }
  };

  return (
    <Card title="Book an appointment">
      <Form label="Book appointment" onSubmit={submit}>
        <FormRow>
          {closedSet ? (
            <SelectField
              label="Dock door"
              value={door}
              onChange={setDoor}
              options={docks.items.map((d) => ({ value: d.doorCode, label: d.doorCode }))}
              required
            />
          ) : (
            <TextField label="Dock door" value={door} onChange={setDoor} required hint="Any door code (the door list is not enforced)" />
          )}
          <TextField label="Carrier" value={carrier} onChange={setCarrier} required />
        </FormRow>
        <FormRow>
          <TextField
            label="Window start (UTC)"
            type="datetime-local"
            value={start}
            onChange={setStart}
            required
            hint={`The board shows ${day}`}
          />
          <TextField
            label="Window end (UTC)"
            type="datetime-local"
            value={end}
            onChange={setEnd}
            required
            hint="At most 4 hours after the start"
          />
        </FormRow>
        <TextField
          label="ASN numbers"
          value={asns}
          onChange={setAsns}
          required
          hint="One or more registered ASNs, separated by commas or spaces"
        />
        <div>
          <SubmitButton disabled={saving}>{saving ? "Booking…" : "Book appointment"}</SubmitButton>
        </div>
        <InlineError message={invalid} />
        {problem && <ProblemAlert error={problem} />}
        {booked && (
          <InlineSuccess
            message={`Booked ${booked.doorCode} for ${booked.carrier}, ${utcTime(booked.windowStart)}–${utcTime(booked.windowEnd)}Z.`}
          />
        )}
      </Form>
    </Card>
  );
}
