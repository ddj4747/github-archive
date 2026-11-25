"use strict";

const $ = (selector, root = document) => root.querySelector(selector);
const $$ = (selector, root = document) => Array.from(root.querySelectorAll(selector));
const pageSize = 20;
const targetPageSize = 5;
const peopleLookahead = 32;
const searchIdleMs = 900;
const state = { route: "overview", eventOffset: 0, refOffset: 0, targetOffset: 0, userOffset: 0, eventMore: false, refMore: false, targetMore: false, userMore: false, eventFilters: {}, refFilters: { sort: "newest" }, config: null, targets: null, repo: null, historyOffset: 0, historyMore: false, selectedSHA: "", sourcePath: "", sourceRequest: 0, searchRequest: 0, refreshing: false };
const titles = { overview: "Overview", activity: "Activity", refs: "Ref history", targets: "Targets", repository: "Repository" };

function node(tag, className, value) {
  const element = document.createElement(tag);
  if (className) element.className = className;
  if (value !== undefined) element.textContent = String(value);
  return element;
}
function clear(element) { element.replaceChildren(); }
function text(selector, value) { $(selector).textContent = String(value); }
function showState(container, message, error = false) {
  delete container.dataset.renderKey;
  clear(container);
  container.append(node("div", "state" + (error ? " error" : ""), message));
}
function notice(message, error = false) {
  const el = $("#notice");
  el.textContent = message;
  el.classList.toggle("error", error);
  el.hidden = false;
}
function friendlyError(error) { return error instanceof Error ? error.message : "An unexpected error occurred."; }
async function request(path, options) {
  const response = await fetch(path, options);
  if (!response.ok) {
    let message = "Request failed (" + response.status + ").";
    const type = response.headers.get("content-type") || "";
    try {
      if (type.includes("json")) {
        const result = await response.json();
        if (typeof result.error === "string" && result.error.length < 500) message = result.error;
      } else {
        const result = await response.text();
        if (result && result.length < 500) message = result;
      }
    } catch (_) { /* Keep the status-based error. */ }
    const error = new Error(message);
    error.status = response.status;
    error.retryAfter = response.headers.get("Retry-After") || "";
    throw error;
  }
  return response.status === 204 ? null : response.json();
}
function query(values) {
  const params = new URLSearchParams();
  for (const [key, value] of Object.entries(values)) if (value !== "" && value !== null && value !== undefined) params.set(key, String(value));
  return params.toString();
}
function compactTime(value) {
  if (!value) return "Time unavailable";
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return String(value);
  return new Intl.DateTimeFormat(undefined, { year: "numeric", month: "short", day: "numeric", hour: "2-digit", minute: "2-digit" }).format(date);
}
function number(value) { return Number.isFinite(Number(value)) ? new Intl.NumberFormat().format(Number(value)) : "—"; }
function eventName(value) { return String(value || "Activity").replace(/Event$/, "").replace(/([a-z])([A-Z])/g, "$1 $2"); }
function targetName(owner, name) { return [owner, name].filter(Boolean).join("/"); }
function captureListFocus(container) {
  const active = document.activeElement;
  if (!active || !container.contains(active)) return null;
  const row = active.closest("[data-row-key]");
  if (!row) return null;
  if (active.tagName !== "BUTTON") return null;
  const buttons = Array.from(row.querySelectorAll("button")).filter(button => button.textContent === active.textContent);
  return { key: row.dataset.rowKey, label: active.textContent, index: buttons.indexOf(active) };
}
function restoreListFocus(container, saved) {
  if (!saved || saved.index < 0) return;
  const row = Array.from(container.querySelectorAll("[data-row-key]")).find(item => item.dataset.rowKey === saved.key);
  const button = row && Array.from(row.querySelectorAll("button")).filter(item => item.textContent === saved.label)[saved.index];
  if (button) button.focus({ preventScroll: true });
}

function setupTargetControls() {
  const repoFilters = node("form", "target-filters"); repoFilters.id = "target-filters"; repoFilters.setAttribute("role", "search");
  const repoLabel = node("label", "", "Search repositories"), repoInput = node("input");
  repoInput.name = "q"; repoInput.placeholder = "Owner or repository name"; repoInput.autocomplete = "off"; repoLabel.append(repoInput);
  const statusLabel = node("label", "", "Monitoring status"), status = node("select"); status.name = "status";
  for (const [value, label] of [["", "All statuses"], ["configured", "Configured"], ["discovered", "Discovered"], ["paused", "Paused"], ["archived", "Archive only"]]) {
    const option = node("option", "", label); option.value = value; status.append(option);
  }
  statusLabel.append(status); repoFilters.append(repoLabel, statusLabel); $("#configured-repos").before(repoFilters);
  const userFilter = node("div", "target-filters"), userLabel = node("label", "", "Search people"), userInput = node("input");
  userInput.id = "target-user-filter"; userInput.placeholder = "Filter configured people"; userInput.autocomplete = "off";
  userLabel.append(userInput); userFilter.append(userLabel); $("#configured-users").before(userFilter);
  let timer;
  const changed = offset => {
    clearTimeout(timer);
    state.targetRequest = (state.targetRequest || 0) + 1;
    state[offset] = 0;
    if (offset === "userOffset") state.userPageStarts = [];
    state.targetTyping = true;
    timer = setTimeout(() => { state.targetTyping = false; loadTargets(); }, searchIdleMs);
  };
  repoInput.addEventListener("input", () => changed("targetOffset"));
  status.addEventListener("change", () => changed("targetOffset"));
  userInput.addEventListener("input", () => changed("userOffset"));
  repoFilters.addEventListener("submit", event => { event.preventDefault(); clearTimeout(timer); state.targetTyping = false; state.targetOffset = 0; loadTargets(); });
}

function setupRecentRefs() {
  const section = node("article", "panel recent-refs"), heading = node("div", "panel-heading");
  const description = node("div"); description.append(node("div", "section-kicker", "OBSERVED IN LOCAL MIRRORS"), node("h2", "", "Git ref changes"), node("p", "panel-description", "Changes detected by the archiver can appear here before GitHub publishes an activity event."));
  heading.append(description);
  const note = node("div", "panel-foot"); note.id = "recent-refs-note";
  const list = node("div", "ref-list"); list.id = "recent-refs-list";
  section.append(heading, list, note); $("#event-filter").closest(".panel").before(section);
}

function eventRow(event) {
  const row = node("div", "activity-row");
  row.dataset.rowKey = JSON.stringify([event.scope, event.event_id]);
  const icon = node("div", "activity-icon", "◷"); icon.setAttribute("aria-hidden", "true");
  const body = node("div", "activity-body");
  const title = node("button", "event-open row-title", eventName(event.event_type)); title.type = "button";
  title.setAttribute("aria-label", "View " + eventName(event.event_type) + " event " + (event.event_id || ""));
  title.addEventListener("click", () => openEvent(event));
  const detail = node("div", "row-subtitle");
  if (event.login) detail.append(document.createTextNode(event.login + " · "));
  if (event.repo_owner && event.repo_name) detail.append(repoLink({ owner: event.repo_owner, name: event.repo_name }));
  else detail.append(document.createTextNode(event.scope || "GitHub"));
  if (event.event_id) detail.append(document.createTextNode(" · #" + event.event_id));
  body.append(title, detail);
  row.append(icon, body, node("time", "row-time", compactTime(event.at)));
  return row;
}
function openEvent(event) {
  const target = $("#event-state");
  target.hidden = false; target.replaceChildren(node("div", "", "Checking whether this event has an archived commit…"));
  const requestID = state.eventStateRequest = (state.eventStateRequest || 0) + 1;
  text("#dialog-title", eventName(event.event_type));
  const meta = $("#dialog-meta");
  meta.textContent = [event.scope, event.event_id ? "Event " + event.event_id : "", compactTime(event.at)].filter(Boolean).join("  ·  ");
  let raw = event.raw || "No raw event available.";
  let parsed;
  try { parsed = JSON.parse(raw); raw = JSON.stringify(parsed, null, 2); } catch (_) { /* Show the original text. */ }
  const link = $("#event-repository");
  const repoName = event.repo_owner && event.repo_name ? targetName(event.repo_owner, event.repo_name) :
    parsed?.repo?.name || (event.scope?.startsWith("repo:") ? event.scope.slice(5) : "");
  const validRepo = typeof repoName === "string" && /^[\w.-]+\/[\w.-]+$/.test(repoName);
  link.hidden = !validRepo;
  link.onclick = null;
  if (validRepo) {
    const [owner, name] = repoName.split("/");
    link.textContent = "Open repository · " + repoName;
    link.onclick = () => {
      $("#event-dialog").close();
      openRepository({ owner, name });
    };
  }
  text("#dialog-raw", raw);
  $("#event-dialog").showModal();
  request("/api/event-state?" + query({ scope: event.scope, event_id: event.event_id })).then(result => {
    if (requestID === state.eventStateRequest && $("#event-dialog").open) renderArchivedState(target, result);
  }).catch(error => {
    if (requestID === state.eventStateRequest && $("#event-dialog").open) target.textContent = "Could not check archived state: " + friendlyError(error);
  });
}
function renderEvents(container, items, empty) {
  const key = JSON.stringify(items);
  if (container.dataset.renderKey === key) return;
  const focus = captureListFocus(container);
  clear(container);
  if (!Array.isArray(items) || !items.length) return showState(container, empty);
  for (const event of items) container.append(eventRow(event));
  container.dataset.renderKey = key;
  restoreListFocus(container, focus);
}
function refRow(ref) {
  const row = node("div", "ref-row");
  row.dataset.rowKey = JSON.stringify([ref.owner, ref.name, ref.ref_name, ref.old_sha, ref.new_sha]);
  const icon = node("div", "activity-icon", "⑂"); icon.setAttribute("aria-hidden", "true");
  const body = node("div", "ref-body");
  body.append(repoLink(ref), node("div", "row-subtitle", ref.ref_name || "Unknown ref"));
  const shas = node("div", "sha");
  shas.append(document.createTextNode(ref.old_sha || "∅"), node("span", "sha-sep", "→"), document.createTextNode(ref.new_sha || "∅"));
  body.append(shas);
  const actions = node("div", "ref-state-actions");
  const output = node("div", "archived-state-result");
  let stateRequest = 0;
  const check = async side => {
    const id = ++stateRequest;
    output.textContent = "Checking archived " + side + " state…";
    try {
      // Empty IDs are part of a ref change's identity and must reach the server.
      const params = new URLSearchParams({ owner: ref.owner || "", name: ref.name || "", ref_name: ref.ref_name || "", old_sha: ref.old_sha || "", new_sha: ref.new_sha || "", side });
      const result = await request("/api/ref-state?" + params.toString());
      if (id === stateRequest) renderArchivedState(output, result);
    } catch (error) { if (id === stateRequest) output.textContent = "Could not check archived state: " + friendlyError(error); }
  };
  actions.append(action("Before", () => check("before")), action("After", () => check("after")));
  body.append(actions, output);
  const source = { observation: "Observed", github_push: "GitHub push", tip_commit: "Tip commit date" }[ref.time_source];
  if (source && ref.timeline_at) {
    const date = node("time", "row-subtitle", source + " · " + compactTime(ref.timeline_at));
    date.dateTime = ref.timeline_at;
    body.append(date);
  } else if (Object.hasOwn(ref, "detected_at")) {
    body.append(node("div", "row-subtitle", "Change date unavailable"));
  }
  row.append(icon, body, node("span", "kind " + (["force_push", "delete"].includes(ref.kind) ? ref.kind : ""), String(ref.kind || "change").replaceAll("_", " ")));
  return row;
}
function repoLink(repo) {
  const repoButton = node("button", "repo-link", targetName(repo.owner, repo.name));
  repoButton.type = "button";
  repoButton.addEventListener("click", () => openRepository(repo));
  return repoButton;
}
function repoRow(repo, options = {}) {
  const row = node("div", "target-row");
  row.dataset.rowKey = JSON.stringify([repo.owner, repo.name]);
  const icon = node("div", "target-icon", "▣"); icon.setAttribute("aria-hidden", "true");
  const body = node("div", "target-body");
  body.append(repoLink(repo));
  const subtitle = [options.source, options.inactive ? "Not monitored · archive retained" : options.excluded ? (options.manual ? "Auto-monitoring paused · configured target active" : "Auto-monitoring paused · archive retained") : "", repo.last_synced ? "Last successful sync " + compactTime(repo.last_synced) : "", repo.last_polled ? "Last attempt " + compactTime(repo.last_polled) : ""].filter(Boolean).join(" · ");
  body.append(node("div", "row-subtitle", subtitle || "Repository mirror"));
  row.append(icon, body);
  if (options.actions) {
    const actions = node("div", "target-actions");
    actions.append(action("View log", () => viewLog({ owner: repo.owner, name: repo.name }, targetName(repo.owner, repo.name))));
    actions.append(action("Download bundle", () => bundle(repo)));
    if (options.manual) actions.append(action("Edit", () => editTarget("repo", repo)));
    if (options.manual) actions.append(action("Remove", () => removeRepo(repo), true));
    if (options.user) actions.append(action(options.excluded ? "Resume auto-monitoring" : "Stop auto-monitoring", () => toggleExcluded(repo, options.user, !options.excluded)));
    row.append(actions);
  }
  return row;
}
function action(label, handler, danger = false) {
  const button = node("button", "target-action" + (danger ? " danger" : ""), label);
  button.type = "button";
  button.addEventListener("click", handler);
  return button;
}
function bundle(repo) {
  window.location.assign("/api/repository/bundle?" + query({ owner: repo.owner, name: repo.name }));
}
function archivedDownload(value, endpoint) {
  window.location.assign("/api/repository/" + endpoint + "?" + query({ owner: value.owner, name: value.name, sha: value.sha }));
}
function renderArchivedState(container, value) {
  clear(container);
  if (!value.available) {
    container.append(node("div", "archived-state-explanation", value.reason || "No archived commit is available for this state."));
    return;
  }
  container.append(node("div", "archived-state-explanation", "Files at " + value.sha.slice(0, 12) + (value.ref ? " on " + value.ref : "") + ". Other refs may have differed."));
  const actions = node("div", "archived-state-buttons");
  actions.append(action("Browse files", () => {
    if ($("#event-dialog").open) $("#event-dialog").close();
    openRepository({ owner: value.owner, name: value.name }, value);
  }), action("Download ZIP", () => archivedDownload(value, "snapshot")), action("Download commit bundle", () => archivedDownload(value, "commit-bundle")));
  container.append(actions);
}
async function viewLog(target, name) {
  try {
    const result = await request("/api/log?" + query(target));
    text("#dialog-title", "Target log");
    text("#dialog-meta", name);
    text("#dialog-raw", result.text || "No log entries yet.");
    $("#event-repository").hidden = true;
    state.eventStateRequest = (state.eventStateRequest || 0) + 1;
    $("#event-state").hidden = true;
    $("#event-dialog").showModal();
  } catch (error) { notice("Could not load target log: " + friendlyError(error), true); }
}
function pageButton(prefix, offset, more, size = pageSize) {
  text("#" + prefix + "-page-label", "Page " + (Math.floor(offset / size) + 1));
  $("#" + prefix + "-prev").disabled = offset <= 0;
  $("#" + prefix + "-next").disabled = !more;
}
async function loadStats(quiet = false) {
  try {
    const result = await request("/api/stats");
    text("#stat-events", number(result.events)); text("#nav-events", number(result.events));
    text("#stat-refs", number(result.refs)); text("#stat-repos", number(result.repositories)); text("#stat-users", number(result.users));
    const label = $("#worker-label"), dot = $("#worker-dot");
    const detail = node("small", "", result.worker_running ? "Collecting configured targets" : "Start the archiver to collect activity");
    label.replaceChildren(document.createTextNode(result.worker_running ? "Archiver running" : "Archiver stopped"), detail);
    dot.classList.toggle("offline", !result.worker_running);
    $("#worker-guidance").hidden = Boolean(result.worker_running);
    return true;
  } catch (error) {
    const label = $("#worker-label");
    label.replaceChildren(document.createTextNode("Status unavailable"), node("small", "", "Could not reach the local panel"));
    $("#worker-dot").classList.add("offline");
    if (!quiet) notice("Could not load archive totals: " + friendlyError(error), true);
    return false;
  }
}
async function loadOverview(quiet = false) {
  const requestID = state.overviewRequest = (state.overviewRequest || 0) + 1;
  const activity = $("#recent-activity");
  if (!quiet) showState(activity, "Loading activity…");
  const stats = loadStats(quiet);
  const recent = request("/api/events?" + query({ limit: 6, offset: 0 })).then(result => { if (requestID !== state.overviewRequest) return false; renderEvents(activity, result.items, "No activity recorded yet. Events will appear here as targets are polled."); return true; })
    .catch(error => { if (requestID === state.overviewRequest && !quiet) showState(activity, "Could not load activity: " + friendlyError(error), true); return false; });
  const targets = request("/api/targets?limit=6&offset=0").then(result => {
    if (requestID !== state.overviewRequest) return false;
    const container = $("#overview-repos"), repos = asArray(result.repositories), key = JSON.stringify(repos), focus = captureListFocus(container);
    if (container.dataset.renderKey === key) return true;
    clear(container);
    if (!repos.length) showState(container, "No repositories yet. Add a target to get started.");
    else { for (const repo of repos) container.append(repoRow(repo, { source: repo.status || repo.source })); container.dataset.renderKey = key; restoreListFocus(container, focus); }
    return true;
  }).catch(error => { if (requestID === state.overviewRequest && !quiet) showState($("#overview-repos"), "Could not load targets: " + friendlyError(error), true); return false; });
  const results = await Promise.all([stats, recent, targets]);
  return results.every(Boolean);
}
async function loadEvents(quiet = false) {
  loadRecentRefs(quiet);
  const requestID = state.eventRequest = (state.eventRequest || 0) + 1;
  const list = $("#event-list"); if (!quiet) showState(list, "Loading events…");
  try {
    const result = await request("/api/events?" + query({ ...state.eventFilters, limit: pageSize, offset: state.eventOffset }));
    if (requestID !== state.eventRequest) return false;
    renderEvents(list, result.items, "No events match these filters.");
    state.eventMore = Boolean(result.has_more);
  } catch (error) { if (requestID !== state.eventRequest) return false; if (!quiet) { state.eventMore = false; showState(list, "Could not load events: " + friendlyError(error), true); } return false; }
  pageButton("event", state.eventOffset, state.eventMore);
  return true;
}
async function loadRecentRefs(quiet = false) {
  const requestID = state.recentRefsRequest = (state.recentRefsRequest || 0) + 1;
  const list = $("#recent-refs-list"), note = $("#recent-refs-note"), f = state.eventFilters;
  if ((f.type && f.type !== "PushEvent") || f.login) {
    showState(list, "Ref observations do not have a GitHub actor or event type and are hidden for these filters.");
    note.textContent = "Clear the event type and actor filters to see recent ref changes.";
    return true;
  }
  note.textContent = "Date filters use the date shown on each row. Event text and scope apply only to GitHub events below.";
  if (!quiet) showState(list, "Loading recent ref changes…");
  try {
    const result = await request("/api/recent-ref-changes?" + query({
      repo_owner: f.repo_owner, repo_name: f.repo_name, from: f.from, before: f.before, sort: f.sort, limit: 6, offset: 0
    }));
    if (requestID !== state.recentRefsRequest) return false;
    const key = JSON.stringify(result.items), focus = captureListFocus(list);
    if (list.dataset.renderKey === key) return true;
    clear(list);
    const items = asArray(result.items);
    if (!items.length) showState(list, "No matching ref observations yet.");
    else { for (const ref of items) list.append(refRow({ ...ref, detected_at: ref.detected_at || "" })); list.dataset.renderKey = key; restoreListFocus(list, focus); }
    return true;
  } catch (error) { if (requestID !== state.recentRefsRequest) return false; if (!quiet) showState(list, "Could not load recent ref observations: " + friendlyError(error), true); return false; }
}
async function loadRefs(quiet = false) {
  const requestID = state.refRequest = (state.refRequest || 0) + 1;
  const list = $("#ref-list"); if (!quiet) showState(list, "Loading ref changes…");
  try {
    const result = await request("/api/refs?" + query({ ...state.refFilters, limit: pageSize, offset: state.refOffset }));
    if (requestID !== state.refRequest) return false;
    const key = JSON.stringify(result.items), focus = captureListFocus(list);
    if (list.dataset.renderKey !== key) {
      clear(list);
      if (!Array.isArray(result.items) || !result.items.length) {
        const dates = state.refFilters;
        const datedOnly = (dates.from || dates.before) && !dates.include_undated;
        showState(list, datedOnly ?
          "No dated changes match. Select Include undated changes and apply the filters to see older records." :
          "No ref changes match these filters.");
      }
      else { for (const ref of result.items) list.append(refRow(ref)); list.dataset.renderKey = key; restoreListFocus(list, focus); }
    }
    state.refMore = Boolean(result.has_more);
  } catch (error) { if (requestID !== state.refRequest) return false; if (!quiet) { state.refMore = false; showState(list, "Could not load ref changes: " + friendlyError(error), true); } return false; }
  pageButton("ref", state.refOffset, state.refMore);
  return true;
}
function same(a, b) { return String(a || "").toLowerCase() === String(b || "").toLowerCase(); }
function asArray(value) { return Array.isArray(value) ? value : []; }
function renderTargets() {
  const config = state.config || {}, targets = state.targets || {};
  const repos = asArray(targets.repositories), users = asArray(config.users), manual = asArray(config.repositories);
  const repoList = $("#configured-repos"), repoFocus = captureListFocus(repoList); clear(repoList);
  for (const repo of repos) {
    const isManual = manual.some(item => same(item.owner, repo.owner) && same(item.name, repo.name));
    const sourceUser = users.find(item => same(item.login, repo.source)) || users.find(item => same(item.login, repo.owner));
    const user = sourceUser && sourceUser.discover_repositories ? sourceUser : null;
    const excluded = !!user && asArray(user.exclude_repositories).some(name => same(name, repo.name));
    const status = repo.status || (isManual ? "configured" : "discovered");
    const label = { configured: "Configured", discovered: "Discovered", paused: "Paused", archived: "Archive only" }[status] || status;
    repoList.append(repoRow(repo, { actions: true, source: label + (repo.source && repo.source !== "configured" ? " · " + repo.source : ""), manual: isManual, user, excluded, inactive: status === "archived" }));
  }
  if (!repoList.childElementCount) showState(repoList, "No repositories match these filters.");
  restoreListFocus(repoList, repoFocus);
  text("#repo-count", number(repos.length) + " shown");
  state.targetMore = Boolean(targets.has_more);
  pageButton("target", state.targetOffset, state.targetMore, targetPageSize);
  const userList = $("#configured-users"), userFocus = captureListFocus(userList); clear(userList);
  const matchingUsers = asArray(targets.users).map(target => users.find(user => same(user.login, target.login)) || target);
  if (!matchingUsers.length) showState(userList, "No people match this filter.");
  for (const user of matchingUsers) {
    const row = node("div", "target-row");
    row.dataset.rowKey = user.login;
    const icon = node("div", "target-icon", "◎"); icon.setAttribute("aria-hidden", "true");
    const body = node("div", "target-body");
    body.append(node("div", "row-title", user.login), node("div", "row-subtitle", [user.discover_repositories ? "Repository discovery" : "Public activity", user.authenticated_events ? "Authenticated events" : "", user.poll_interval || "Default interval"].filter(Boolean).join(" · ")));
    const actions = node("div", "target-actions"); actions.append(action("View log", () => viewLog({ login: user.login }, user.login)), action("Edit", () => editTarget("user", user)), action("Remove", () => removeUser(user), true));
    row.append(icon, body, actions); userList.append(row);
  }
  fitPeoplePage();
  restoreListFocus(userList, userFocus);
  const form = $("#settings-form");
  for (const name of ["token_env", "poll_interval", "data_root"]) $("[name=" + name + "]", form).value = config[name] || "";
}
function fitPeoplePage() {
  const list = $("#configured-users");
  if (!list.clientHeight) return;
  const rows = $$(".target-row", list);
  list.scrollTop = 0;
  for (const row of rows) row.hidden = false;
  const top = list.getBoundingClientRect().top;
  const firstOverflow = rows.findIndex(row => row.getBoundingClientRect().bottom - top > list.clientHeight + 0.5);
  const visible = firstOverflow < 0 ? rows.length : Math.max(1, firstOverflow);
  for (const row of rows.slice(visible)) row.hidden = true;
  state.userVisible = visible;
  state.userMore = visible < rows.length || Boolean(state.targets?.users_have_more);
  const previousPages = state.userPageStarts?.length || 0;
  text("#user-count", number(visible) + " shown");
  text("#target-user-page-label", "Page " + (previousPages + 1));
  $("#target-user-prev").disabled = previousPages === 0;
  $("#target-user-next").disabled = !state.userMore;
}
async function loadTargets(quiet = false) {
  if (quiet && state.targetTyping) return true;
  const requestID = state.targetRequest = (state.targetRequest || 0) + 1;
  const form = $("#target-filters"), user = $("#target-user-filter");
  try {
    const params = { ...filters(form), user_q: user.value.trim(), limit: targetPageSize, offset: state.targetOffset, user_limit: peopleLookahead, user_offset: state.userOffset };
    const [targets, config] = await Promise.all([request("/api/targets?" + query(params)), request("/api/config")]);
    if (requestID !== state.targetRequest) return false;
    const emptyRepoPage = params.offset > 0 && !asArray(targets.repositories).length;
    const emptyUserPage = params.user_offset > 0 && !asArray(targets.users).length;
    if (emptyRepoPage || emptyUserPage) {
      if (emptyRepoPage) state.targetOffset = 0;
      if (emptyUserPage) { state.userOffset = 0; state.userPageStarts = []; }
      return loadTargets(quiet);
    }
    state.targets = targets; state.config = config; renderTargets(); return true;
  } catch (error) {
    if (requestID !== state.targetRequest) return false;
    if (!quiet) for (const selector of ["#overview-repos", "#configured-repos", "#configured-users"]) showState($(selector), "Could not load targets: " + friendlyError(error), true);
    return false;
  }
}
async function updateConfig(change, success) {
  try {
    const config = await request("/api/config");
    config.repositories = asArray(config.repositories); config.users = asArray(config.users);
    change(config);
    await request("/api/config", { method: "PUT", headers: { "Content-Type": "application/json" }, body: JSON.stringify(config) });
    notice(success + " Changes apply while the archiver is running. Existing archive data is retained.");
    await loadTargets();
    return true;
  } catch (error) { notice("Could not save configuration: " + friendlyError(error), true); return false; }
}
function removeRepo(repo) {
  if (!window.confirm("Remove " + targetName(repo.owner, repo.name) + " from configured repositories? Its archive will be kept.")) return;
  updateConfig(config => { config.repositories = config.repositories.filter(item => !(same(item.owner, repo.owner) && same(item.name, repo.name))); }, "Repository removed from configuration.");
}
function removeUser(user) {
  if (!window.confirm("Remove " + user.login + " from configured people? Their archive will be kept.")) return;
  updateConfig(config => { config.users = config.users.filter(item => !same(item.login, user.login)); }, "Person removed from configuration.");
}
function toggleExcluded(repo, user, excluded) {
  updateConfig(config => {
    const current = config.users.find(item => same(item.login, user.login));
    if (!current) throw new Error("The configured person no longer exists.");
    current.exclude_repositories = asArray(current.exclude_repositories).filter(name => !same(name, repo.name));
    if (excluded) current.exclude_repositories.push(repo.name);
  }, excluded ? "Auto-monitoring paused for " + targetName(repo.owner, repo.name) + "." : "Auto-monitoring resumed for " + targetName(repo.owner, repo.name) + ".");
}
function editTarget(kind, target) {
  const form = $("#edit-form");
  const current = kind === "repo" ? asArray(state.config.repositories).find(repo => same(repo.owner, target.owner) && same(repo.name, target.name)) : asArray(state.config.users).find(user => same(user.login, target.login));
  if (!current) { notice("This target is no longer configured.", true); return; }
  state.edit = { kind, target };
  text("#edit-title", kind === "repo" ? targetName(target.owner, target.name) : target.login);
  form.elements.poll_interval.value = current.poll_interval || "";
  form.elements.discover_repositories.checked = Boolean(current.discover_repositories);
  form.elements.authenticated_events.checked = Boolean(current.authenticated_events);
  $("#edit-user-options").hidden = kind !== "user";
  $("#edit-dialog").showModal();
}
function filters(form) {
  const data = new FormData(form), result = {};
  for (const [key, value] of data.entries()) result[key] = String(value).trim();
  return result;
}
const optionFilters = [];
function setupOptionFilters() {
  const definitions = { event_types: "All event types", users: "All actors", repositories: "All repositories" };
  for (const container of $$(".option-filter")) {
    const kind = container.dataset.option, search = $(".option-search", container), select = $("select", container);
    const previous = $(".option-prev", container), next = $(".option-next", container), status = $(".option-status", container), paging = $(".option-paging", container);
    if (kind === "users") $("label", container).textContent = "User (actor)";
    select.after(search);
    search.hidden = true; paging.hidden = true;
    let offset = 0, serial = 0, controller, timer, more = false;
    const prepare = () => { const id = ++serial; if (controller) controller.abort(); controller = new AbortController(); return id; };
    const render = data => {
      const selected = select.value, values = asArray(data[kind]);
      clear(select); const all = node("option", "", definitions[kind]); all.value = ""; select.append(all);
      for (const value of values) {
        const name = kind === "repositories" ? targetName(value.owner, value.name) : String(value);
        if (!name) continue;
        const option = node("option", "", name); option.value = name; select.append(option);
      }
      if (selected && !Array.from(select.options).some(option => option.value === selected)) {
        const option = node("option", "", selected); option.value = selected; select.append(option);
      }
      select.value = selected;
      more = data.more ? Boolean(data.more[kind]) : Boolean(data.has_more);
      previous.disabled = offset === 0; next.disabled = !more;
      const showTools = more || offset > 0 || Boolean(search.value.trim());
      search.hidden = !showTools; paging.hidden = !showTools;
      status.textContent = "Page " + (Math.floor(offset / 100) + 1) + (values.length ? " · " + values.length + " shown" : " · no matches");
    };
    const load = async () => {
      const id = prepare();
      status.textContent = "Loading…"; previous.disabled = true; next.disabled = true;
      try {
        const data = await request("/api/filter-options?" + query({ q: search.value.trim(), limit: 100, offset }), { signal: controller.signal });
        if (id !== serial) return;
        render(data);
      } catch (error) {
        if (id !== serial || error.name === "AbortError") return;
        status.textContent = "Options unavailable: " + friendlyError(error);
        previous.disabled = offset === 0;
      }
    };
    search.addEventListener("input", () => { clearTimeout(timer); serial++; if (controller) controller.abort(); offset = 0; timer = setTimeout(load, 300); });
    previous.addEventListener("click", () => { offset = Math.max(0, offset - 100); load(); });
    next.addEventListener("click", () => { offset += 100; load(); });
    container.reloadOptions = () => { clearTimeout(timer); offset = 0; load(); };
    optionFilters.push({ prepareShared: () => { if (search.value.trim() || offset > 0) return null; clearTimeout(timer); return prepare(); }, renderShared: (id, data) => { if (id !== null && id === serial) render(data); }, failShared: (id, message) => { if (id !== null && id === serial) { paging.hidden = false; status.textContent = message; } }, reset: () => { offset = 0; search.value = ""; } });
  }
}
async function loadFilterOptions() {
  const ids = optionFilters.map(option => option.prepareShared());
  try {
    const data = await request("/api/filter-options?limit=100&offset=0");
    optionFilters.forEach((option, i) => option.renderShared(ids[i], data));
  } catch (error) {
    optionFilters.forEach((option, i) => option.failShared(ids[i], "Options unavailable: " + friendlyError(error)));
  }
}
function openRepository(repo, archived = null) {
  resetRepository(repo);
  if (archived) selectCommit({ sha: archived.sha, subject: "Archived commit tree" + (archived.ref ? " · " + archived.ref : "") });
  const fragment = "#repository?" + query(state.repo);
  if (location.hash === fragment) route(); else location.hash = fragment;
}
function resetRepository(repo) {
  state.repo = { owner: String(repo.owner), name: String(repo.name) };
  state.historyOffset = 0; state.historyMore = false; state.historyDataKey = ""; state.historyCommits = [];
  state.historyFocus = null; state.selectedSHA = ""; state.sourcePath = "";
  state.repoChangeOffset = 0; state.repoChangeRequest = 0;
  state.repoChangeKey = ""; state.refFocusRequest = 0;
  $("#repo-change-kind").value = "rewrites";
  $("#repo-change-pages").hidden = true;
  text("#history-view-label", "All archived commits");
  $("#history-show-all").hidden = true;
  clear($("#history-focus-tips"));
  state.sourceRequest++;
  text("#repository-title", targetName(repo.owner, repo.name));
  text("#source-title", "Select a commit");
  text("#source-description", "Choose a commit to explore its archived files.");
  $("#snapshot-download").disabled = true;
  $("#commit-bundle-download").disabled = true;
  clear($("#source-breadcrumbs"));
  showState($("#source-content"), "Select a commit from the history graph.");
}
function repoQuery(extra = {}) { return { owner: state.repo.owner, name: state.repo.name, ...extra }; }
function focusHistory(ref, before, after) {
  state.historyFocus = { tip: before || after, compare: before && after !== before ? after : "" };
  state.historyOffset = 0;
  const label = before && after ? "Before and after " : before ? "Before " : "After ";
  text("#history-view-label", label + (ref.kind === "delete" ? "deletion" : "force-push") + " · " + ref.ref_name);
  $("#history-show-all").hidden = false;
  const tips = $("#history-focus-tips");
  clear(tips);
  for (const [side, sha] of [["Before", before], ["After", after]]) {
    if (sha) tips.append(action(side + " · " + sha.slice(0, 12), () => selectCommit({ sha, subject: ref.ref_name + " · " + side.toLowerCase() })));
  }
  loadHistory();
}
function repoChangeRow(ref) {
  const row = node("div", "repo-change");
  const details = node("div", "repo-change-details");
  const kind = ref.kind === "delete" ? "Deleted" : "Force-pushed";
  const short = id => id ? id.slice(0, 12) : "deleted";
  details.append(node("div", "row-title", ref.ref_name), node("div", "commit-meta", short(ref.old_sha) + " → " + short(ref.new_sha)));
  const status = node("span", "commit-meta"); status.setAttribute("role", "status");
  const button = action(ref.kind === "delete" ? "Show deleted history" : "Compare before and after", async () => {
    const repo = state.repo;
    const serial = ++state.refFocusRequest;
    button.disabled = true;
    status.textContent = "Looking up archived commits…";
    const lookup = side => request("/api/ref-state?" + query({
      owner: ref.owner, name: ref.name, ref_name: ref.ref_name, old_sha: ref.old_sha, new_sha: ref.new_sha, side
    }));
    try {
      const [old, next] = await Promise.all([lookup("before"), ref.new_sha ? lookup("after") : null]);
      if (repo !== state.repo || serial !== state.refFocusRequest || state.route !== "repository" || !row.isConnected) {
        status.textContent = "";
        return;
      }
      if (!old.available && !next?.available) {
        status.textContent = old.reason || next?.reason || "No retained commit for this change.";
        return;
      }
      status.textContent = ref.new_sha && (!old.available || !next.available) ? "Only one side is retained." : "";
      const selected = old.available ? old : next;
      selectCommit({ sha: selected.sha, subject: ref.ref_name + (old.available ? " before the change" : " after the change") });
      focusHistory(ref, old.available ? old.sha : "", next?.available ? next.sha : "");
      $("#history-view-label").scrollIntoView({ block: "nearest" });
    } catch (error) {
      status.textContent = "Could not open the change. " + friendlyError(error);
    } finally {
      button.disabled = false;
    }
  });
  const controls = node("div", "repo-change-controls");
  controls.append(button, status);
  row.append(node("span", "kind " + ref.kind, kind), details, controls);
  return row;
}
async function loadRepoChanges(quiet = false) {
  const repo = state.repo, offset = state.repoChangeOffset;
  const serial = ++state.repoChangeRequest;
  const list = $("#repo-change-list");
  const kind = $("#repo-change-kind").value;
  if (!quiet) {
    showState(list, "Loading changes…");
    $("#repo-change-pages").hidden = true;
  }
  try {
    const result = await request("/api/refs?" + query(repoQuery({ kind, limit: pageSize, offset })));
    if (repo !== state.repo || serial !== state.repoChangeRequest) return false;
    const key = JSON.stringify([offset, kind, result]);
    if (quiet && (key === state.repoChangeKey || list.contains(document.activeElement) || $("button:disabled", list))) return true;
    clear(list);
    for (const ref of asArray(result.items)) list.append(repoChangeRow(ref));
    if (!list.childElementCount) showState(list, "No matching changes have been recorded for this repository.");
    pageButton("repo-change", offset, result.has_more);
    $("#repo-change-pages").hidden = !offset && !result.has_more;
    state.repoChangeKey = key;
    return true;
  } catch (error) {
    if (!quiet && repo === state.repo && serial === state.repoChangeRequest) showState(list, "Could not load changes. " + friendlyError(error), true);
    return false;
  }
}
function svgNode(tag, attrs) {
  const element = document.createElementNS("http://www.w3.org/2000/svg", tag);
  for (const [name, value] of Object.entries(attrs)) element.setAttribute(name, String(value));
  return element;
}
function drawGraph(commits, container) {
  const old = $(".graph-canvas", container); if (old) old.remove();
  const rows = $$(".commit-row", container); if (!rows.length) return;
  let lanes = [], maxLanes = 1;
  const plans = commits.map(commit => {
    const before = lanes.slice();
    let lane = lanes.indexOf(commit.sha), incoming = lane >= 0;
    if (lane < 0) { lane = lanes.indexOf(null); if (lane < 0) lane = lanes.length; lanes[lane] = commit.sha; }
    const after = lanes.slice(); after[lane] = null;
    const destinations = [];
    for (const parent of asArray(commit.parents)) {
      let to = after.indexOf(parent);
      if (to < 0) { to = after.indexOf(null); if (to < 0) to = after.length; after[to] = parent; }
      destinations.push(to);
    }
    while (after.length && after[after.length - 1] === null) after.pop();
    maxLanes = Math.max(maxLanes, before.length, after.length, lane + 1);
    lanes = after;
    return { before, after, lane, incoming, destinations };
  });
  const width = Math.max(62, maxLanes * 18 + 22);
  container.style.setProperty("--graph-width", (width + 8) + "px");
  const height = container.scrollHeight;
  const graph = svgNode("svg", { class: "graph-canvas", width, height, "aria-hidden": "true" });
  const colors = ["#b5a0ff", "#78cbaa", "#7fb3f0", "#e9bd7d", "#db96ba", "#8acbd2"];
  const x = lane => 18 + lane * 18;
  function line(x1, y1, x2, y2, lane) {
    graph.append(svgNode("path", { d: `M ${x1} ${y1} L ${x2} ${y2}`, fill: "none", stroke: colors[lane % colors.length], "stroke-width": 2, opacity: 0.85 }));
  }
  plans.forEach((plan, i) => {
    const row = rows[i], top = row.offsetTop, mid = top + row.offsetHeight / 2, bottom = top + row.offsetHeight;
    plan.before.forEach((sha, lane) => { if (!sha) return; if (lane === plan.lane) line(x(lane), top, x(lane), mid, lane); else if (plan.after[lane] === sha) line(x(lane), top, x(lane), bottom, lane); });
    plan.destinations.forEach(to => line(x(plan.lane), mid, x(to), bottom, to));
    graph.append(svgNode("circle", { cx: x(plan.lane), cy: mid, r: 5, fill: colors[plan.lane % colors.length], stroke: "#131720", "stroke-width": 2 }));
  });
  container.prepend(graph);
}
function displayTime(value) {
  if (typeof value === "number") return compactTime(value < 1e12 ? value * 1000 : value);
  if (typeof value === "string" && /^\d+$/.test(value)) { const n = Number(value); return compactTime(n < 1e12 ? n * 1000 : n); }
  return compactTime(value);
}
async function loadHistory(quiet = false) {
  if (!state.repo) return;
  const currentRepo = state.repo, offset = state.historyOffset, focus = state.historyFocus, list = $("#commit-list");
  const serial = state.historyRequest = (state.historyRequest || 0) + 1;
  if (!quiet) showState(list, "Loading commit history…");
  try {
    const result = await request("/api/repository/history?" + query(repoQuery({ limit: pageSize, offset, ...focus })));
    if (currentRepo !== state.repo || serial !== state.historyRequest) return false;
    const key = JSON.stringify([offset, focus, result.commits, result.has_more]);
    if (quiet && key === state.historyDataKey) return true;
    const graphScroll = $(".graph-scroll"), previousLeft = graphScroll.scrollLeft, previousTop = graphScroll.scrollTop;
    const pageX = window.scrollX, pageY = window.scrollY;
    const focused = document.activeElement;
    const focusedSHA = focused && list.contains(focused) ? focused.dataset.sha : "";
    clear(list);
    const commits = asArray(result.commits); state.historyMore = Boolean(result.has_more);
    if (!commits.length) showState(list, "No commits are available in this mirror yet.");
    for (const commit of commits) {
      const row = node("div", "commit-row");
      const button = node("button", "commit-select" + (state.selectedSHA === commit.sha ? " selected" : ""));
      button.type = "button";
      button.dataset.sha = commit.sha || "";
      button.append(node("div", "commit-subject", commit.subject || "Untitled commit"));
      button.append(node("div", "commit-meta", [String(commit.sha || "").slice(0, 12), commit.author, displayTime(commit.timestamp)].filter(Boolean).join(" · ")));
      for (const ref of asArray(commit.refs)) button.append(node("span", "ref-tag" + (String(ref).startsWith("refs/keep/") ? " keep" : ""), ref));
      for (const change of asArray(commit.changes)) {
        const label = change.kind === "delete" ? "Before deletion" : change.side === "before" ? "Before force-push" : "After force-push";
        button.append(node("span", "ref-tag history-change " + change.kind, label + " · " + change.ref_name));
      }
      if (commit.changes_truncated) button.append(node("span", "ref-tag", "More recorded changes"));
      button.addEventListener("click", () => selectCommit(commit));
      row.append(button); list.append(row);
    }
    state.historyCommits = commits;
    state.historyDataKey = key;
    if (commits.length) requestAnimationFrame(() => { if (state.repo === currentRepo && state.historyCommits === commits) drawGraph(commits, list); });
    if (focusedSHA) {
      const replacement = $$(".commit-select", list).find(button => button.dataset.sha === focusedSHA);
      if (replacement) replacement.focus({ preventScroll: true });
    }
    graphScroll.scrollLeft = previousLeft;
    graphScroll.scrollTop = previousTop;
    if (quiet) window.scrollTo(pageX, pageY);
    text("#history-page-label", "Page " + (Math.floor(offset / pageSize) + 1));
    $("#history-prev").disabled = offset <= 0; $("#history-next").disabled = !state.historyMore;
    return true;
  } catch (error) {
    if (!quiet && currentRepo === state.repo && serial === state.historyRequest) { showState(list, "Could not load commit history: " + friendlyError(error), true); state.historyMore = false; $("#history-next").disabled = true; }
    return false;
  }
}
function selectCommit(commit) {
  state.selectedSHA = String(commit.sha || ""); state.sourcePath = "";
  for (const el of $$(".commit-select")) el.classList.toggle("selected", el.dataset.sha === state.selectedSHA);
  text("#source-title", "Files at " + state.selectedSHA.slice(0, 12));
  text("#source-description", commit.subject || "Archived commit");
  $("#snapshot-download").disabled = !state.selectedSHA;
  $("#commit-bundle-download").disabled = !state.selectedSHA;
  loadTree("");
}
function sourceCrumbs(path, isFile = false) {
  const bar = $("#source-breadcrumbs"); clear(bar);
  const root = node("button", "crumb-button", "root"); root.type = "button"; root.addEventListener("click", () => loadTree("")); bar.append(root);
  const parts = path ? path.split("/") : [];
  parts.forEach((part, index) => {
    bar.append(node("span", "", "/"));
    if (isFile && index === parts.length - 1) { bar.append(node("span", "", part)); return; }
    const button = node("button", "crumb-button", part); button.type = "button";
    const selectedPath = parts.slice(0, index + 1).join("/");
    button.addEventListener("click", () => loadTree(selectedPath)); bar.append(button);
  });
}
async function loadTree(path, offset = 0) {
  if (!state.repo || !state.selectedSHA) return;
  const requestID = ++state.sourceRequest, sha = state.selectedSHA, repo = state.repo;
  state.sourcePath = path; sourceCrumbs(path);
  const content = $("#source-content"); showState(content, "Loading files…");
  try {
    const result = await request("/api/repository/tree?" + query(repoQuery({ sha, path, offset })));
    if (requestID !== state.sourceRequest || repo !== state.repo || sha !== state.selectedSHA) return;
    clear(content); const entries = asArray(result.entries);
    if (!entries.length) showState(content, "This directory is empty.");
    for (const entry of entries) {
      const row = node("div", "source-entry"), isDirectory = entry.type === "tree" || entry.type === "directory";
      row.append(node("span", "entry-icon", isDirectory ? "▸" : "·"));
      const button = node("button", "", entry.name); button.type = "button";
      button.addEventListener("click", () => {
        if (isDirectory) loadTree(entry.path);
        else if (entry.type === "commit") {
          sourceCrumbs(entry.path, true);
          clear(content);
          content.append(node("div", "source-file-message", "This entry is a Git submodule reference. Its separate repository is not included in this mirror's file preview."));
        } else loadFile(entry.path);
      });
      row.append(button);
      if (Number.isFinite(entry.size) && !isDirectory) row.append(node("span", "entry-size", number(entry.size) + " B"));
      content.append(row);
    }
    if (offset > 0 || result.truncated) {
      const paging = node("div", "source-pagination"), previous = node("button", "button subtle", "Previous entries"), next = node("button", "button subtle", "Next entries");
      previous.type = next.type = "button"; previous.disabled = offset === 0; next.disabled = !result.truncated;
      previous.addEventListener("click", () => loadTree(path, Math.max(0, offset - 500)));
      next.addEventListener("click", () => loadTree(path, offset + 500));
      paging.append(previous, node("span", "", "Entries " + (offset + 1) + "–" + (offset + entries.length)), next); content.append(paging);
    }
  } catch (error) { if (requestID === state.sourceRequest) showState(content, "Could not load files: " + friendlyError(error), true); }
}
async function loadFile(path, offset = 0, previousOffsets = [], binary = false) {
  if (!state.repo || !state.selectedSHA) return;
  const requestID = ++state.sourceRequest, sha = state.selectedSHA, repo = state.repo;
  state.sourcePath = path; sourceCrumbs(path, true);
  const content = $("#source-content"); showState(content, "Loading file…");
  try {
    const result = await request("/api/repository/file?" + query(repoQuery({ sha, path, offset, binary: binary ? 1 : "" })));
    if (requestID !== state.sourceRequest || repo !== state.repo || sha !== state.selectedSHA) return;
    clear(content);
    binary = binary || result.encoding === "base64";
    if (result.encoding === "base64") {
      try {
        const data = atob(result.content || "");
        if (data.length > 4096) throw new Error("Binary preview exceeds its limit");
        const lines = [];
        for (let i = 0; i < data.length; i += 16) {
          const bytes = Array.from(data.slice(i, i + 16), character => character.charCodeAt(0));
          const hex = bytes.map(byte => byte.toString(16).padStart(2, "0")).join(" ").padEnd(47, " ");
          const ascii = bytes.map(byte => byte >= 32 && byte <= 126 ? String.fromCharCode(byte) : ".").join("");
          lines.push((offset + i).toString(16).padStart(8, "0") + "  " + hex + "  |" + ascii + "|");
        }
        content.append(node("div", "source-file-message", "Binary file · hexadecimal and ASCII preview"), node("pre", "source-file", lines.join("\n")));
      } catch (_) { content.append(node("div", "source-file-message", "Binary preview is unavailable. You can download the snapshot to retrieve this file.")); }
    }
    else if (result.encoding === "utf-8" || result.encoding === "utf8" || !result.encoding) content.append(node("pre", "source-file", result.content || ""));
    else content.append(node("div", "source-file-message", "This file uses an unsupported encoding. Download the snapshot to retrieve it."));
    const nextOffset = Number(result.next_offset);
    if (previousOffsets.length || (Number.isFinite(nextOffset) && nextOffset > offset)) {
      const paging = node("div", "source-pagination"), previous = node("button", "button subtle", "Previous part"), next = node("button", "button subtle", "Next part");
      previous.type = next.type = "button"; previous.disabled = !previousOffsets.length; next.disabled = !Number.isFinite(nextOffset) || nextOffset <= offset;
      previous.addEventListener("click", () => loadFile(path, previousOffsets[previousOffsets.length - 1], previousOffsets.slice(0, -1), binary));
      next.addEventListener("click", () => loadFile(path, nextOffset, [...previousOffsets, offset], binary));
      paging.append(previous, node("span", "", "Showing part " + (previousOffsets.length + 1) + " · " + number(result.size) + " bytes total"), next); content.append(paging);
    }
  } catch (error) { if (requestID === state.sourceRequest) showState(content, "Could not load file: " + friendlyError(error), true); }
}
async function searchUsers() {
  const input = $("#user-search"), value = input.value.trim(), status = $("#user-search-status"), results = $("#user-search-results");
  const requestID = ++state.searchRequest; clear(results);
  state.searchResults = []; state.searchIndex = -1; input.setAttribute("aria-expanded", "false"); input.removeAttribute("aria-activedescendant");
  if (state.searchController) state.searchController.abort();
  state.searchController = new AbortController();
  if (!value) { status.textContent = "Enter a username to search."; return; }
  status.textContent = "Searching GitHub…";
  try {
    const data = await request("/api/search/users?" + query({ q: value }), { signal: state.searchController.signal });
    if (requestID !== state.searchRequest) return;
    const users = asArray(data.users);
    state.searchResults = users;
    input.setAttribute("aria-expanded", users.length ? "true" : "false");
    status.textContent = users.length ? "Choose a user to fill in the form." : "No users found. Try another search.";
    for (const [index, user] of users.entries()) {
      const button = node("button", "search-result"); button.type = "button";
      button.id = "user-result-" + index; button.setAttribute("role", "option"); button.setAttribute("aria-selected", "false");
      const avatar = node("span", "search-avatar"), fallback = node("span", "avatar-fallback", String(user.login || "?").slice(0, 1).toUpperCase());
      avatar.append(fallback);
      try {
        const url = new URL(user.avatar_url);
        if (url.protocol === "https:" && url.hostname === "avatars.githubusercontent.com" && !url.username && !url.password && !url.port) {
          const image = node("img", "avatar-image"); image.alt = ""; image.loading = "lazy"; image.referrerPolicy = "no-referrer";
          image.addEventListener("load", () => { fallback.hidden = true; });
          image.addEventListener("error", () => { image.remove(); fallback.hidden = false; });
          image.src = url.href; avatar.append(image);
        }
      } catch (_) { /* Keep the initial when the avatar URL is unavailable. */ }
      button.append(avatar, node("span", "", user.login));
      button.addEventListener("click", () => selectSearchUser(user));
      results.append(button);
    }
  } catch (error) {
    if (requestID === state.searchRequest && error.name !== "AbortError") {
      const retry = error.retryAfter ? " Retry after " + error.retryAfter + (/^\d+$/.test(error.retryAfter) ? " seconds." : ".") : "";
      status.textContent = "Search unavailable: " + friendlyError(error) + retry + " You can still enter a username manually.";
    }
  }
}
function cancelUserSearch(clearStatus = false) {
  clearTimeout(userSearchTimer); ++state.searchRequest;
  if (state.searchController) state.searchController.abort();
  state.searchResults = []; state.searchIndex = -1;
  clear($("#user-search-results"));
  $("#user-search").setAttribute("aria-expanded", "false");
  $("#user-search").removeAttribute("aria-activedescendant");
  if (clearStatus) $("#user-search-status").textContent = "";
}
function selectSearchUser(user) {
  cancelUserSearch();
  $("#user-form").elements.login.value = user.login;
  $("#user-search-status").textContent = "Selected " + user.login + ". Review the options below and add this person.";
  $("#user-form").elements.login.focus();
}
function moveSearchSelection(delta) {
  const users = asArray(state.searchResults); if (!users.length) return;
  state.searchIndex = Math.max(0, Math.min(users.length - 1, (state.searchIndex ?? -1) + delta));
  for (const [index, button] of $$(".search-result", $("#user-search-results")).entries()) {
    const selected = index === state.searchIndex;
    button.classList.toggle("active", selected); button.setAttribute("aria-selected", String(selected));
    if (selected) { $("#user-search").setAttribute("aria-activedescendant", button.id); button.scrollIntoView({ block: "nearest" }); }
  }
}
function route() {
  const previousRoute = state.route;
  const [candidate, params] = (location.hash.slice(1) || "overview").split("?", 2);
  state.route = Object.hasOwn(titles, candidate) ? candidate : "overview";
  if (state.route === "repository" && params) {
    const query = new URLSearchParams(params);
    const owner = query.get("owner"), name = query.get("name");
    if (!owner || !name) state.route = "targets";
    else if (state.repo?.owner !== owner || state.repo?.name !== name) resetRepository({ owner, name });
  }
  if (state.route === "repository" && !state.repo) state.route = "targets";
  if (previousRoute === "targets" && state.route !== "targets") cancelUserSearch(true);
  for (const page of $$(".page")) page.hidden = page.id !== "page-" + state.route;
  for (const link of $$("[data-route]")) {
    const active = link.dataset.route === state.route || (state.route === "repository" && link.dataset.route === "targets");
    link.classList.toggle("active", active);
    if (active) link.setAttribute("aria-current", "page"); else link.removeAttribute("aria-current");
  }
  text("#breadcrumb", titles[state.route]);
  if (state.route === "overview") loadOverview();
  if (state.route === "activity") { loadFilterOptions(); loadEvents(); }
  if (state.route === "refs") loadRefs();
  if (state.route === "targets") loadTargets();
  if (state.route === "repository") { loadHistory(); loadRepoChanges(); }
}

window.addEventListener("hashchange", route);
$("#refresh").addEventListener("click", route);
for (const button of $$('[data-go]')) button.addEventListener("click", () => { location.hash = button.dataset.go; });
$("#close-dialog").addEventListener("click", () => $("#event-dialog").close());
$("#close-edit").addEventListener("click", () => $("#edit-dialog").close());
$("#cancel-edit").addEventListener("click", () => $("#edit-dialog").close());
$("#edit-form").addEventListener("submit", async event => {
  event.preventDefault();
  const form = event.currentTarget, edit = state.edit;
  if (!edit) return;
  const ok = await updateConfig(config => {
    const item = edit.kind === "repo" ? config.repositories.find(repo => same(repo.owner, edit.target.owner) && same(repo.name, edit.target.name)) : config.users.find(user => same(user.login, edit.target.login));
    if (!item) throw new Error("This target is no longer configured.");
    item.poll_interval = form.elements.poll_interval.value.trim();
    if (edit.kind === "user") {
      item.discover_repositories = form.elements.discover_repositories.checked;
      item.authenticated_events = form.elements.authenticated_events.checked;
    }
  }, "Target settings saved.");
  if (ok) $("#edit-dialog").close();
});
function dateFilters(form, dateError) {
  const values = filters(form);
  dateError.hidden = true;
  const start = values.from_date ? new Date(values.from_date + "T00:00:00") : null;
  const end = values.to_date ? new Date(values.to_date + "T00:00:00") : null;
  if ((start && !Number.isFinite(start.getTime())) || (end && !Number.isFinite(end.getTime())) || (start && end && end < start)) {
    dateError.textContent = "Choose a valid date range. The end date must be on or after the start date.";
    dateError.hidden = false;
    return null;
  }
  if (start) values.from = start.toISOString();
  if (end) {
    end.setDate(end.getDate() + 1);
    values.before = end.toISOString();
  }
  delete values.from_date;
  delete values.to_date;
  return values;
}
$("#event-filter").addEventListener("submit", event => {
  event.preventDefault();
  const values = dateFilters(event.currentTarget, $("#event-date-error"));
  if (!values) return;
  state.eventFilters = values;
  if (state.eventFilters.repo) {
    const [owner, name] = state.eventFilters.repo.split("/");
    state.eventFilters.repo_owner = owner; state.eventFilters.repo_name = name;
  }
  delete state.eventFilters.repo;
  state.eventOffset = 0; loadEvents();
});
$("#ref-filter").addEventListener("submit", event => {
  event.preventDefault();
  const values = dateFilters(event.currentTarget, $("#ref-date-error"));
  if (!values) return;
  state.refFilters = values;
  state.refOffset = 0;
  loadRefs();
});
$("#event-filter").addEventListener("reset", () => { $("#event-date-error").hidden = true; state.eventFilters = {}; state.eventOffset = 0; loadEvents(); setTimeout(() => { optionFilters.forEach(option => option.reset()); loadFilterOptions(); }, 0); });
$("#ref-filter").addEventListener("reset", () => {
  $("#ref-date-error").hidden = true;
  state.refFilters = { sort: "newest" };
  state.refOffset = 0;
  loadRefs();
});
$("#event-prev").addEventListener("click", () => { state.eventOffset = Math.max(0, state.eventOffset - pageSize); loadEvents(); });
$("#event-next").addEventListener("click", () => { if (state.eventMore) { state.eventOffset += pageSize; loadEvents(); } });
$("#ref-prev").addEventListener("click", () => { state.refOffset = Math.max(0, state.refOffset - pageSize); loadRefs(); });
$("#ref-next").addEventListener("click", () => { if (state.refMore) { state.refOffset += pageSize; loadRefs(); } });
$("#target-prev").addEventListener("click", () => {
  state.targetOffset = Math.max(0, state.targetOffset - targetPageSize);
  loadTargets();
});
$("#target-next").addEventListener("click", () => {
  if (!state.targetMore) return;
  state.targetOffset += targetPageSize;
  loadTargets();
});
$("#target-user-prev").addEventListener("click", () => {
  if (!state.userPageStarts?.length) return;
  state.userOffset = state.userPageStarts.pop();
  $("#target-user-prev").disabled = $("#target-user-next").disabled = true;
  loadTargets();
});
$("#target-user-next").addEventListener("click", () => {
  if (!state.userMore || !state.userVisible) return;
  (state.userPageStarts ??= []).push(state.userOffset);
  state.userOffset += state.userVisible;
  $("#target-user-prev").disabled = $("#target-user-next").disabled = true;
  loadTargets();
});
$("#history-prev").addEventListener("click", () => { state.historyOffset = Math.max(0, state.historyOffset - pageSize); loadHistory(); });
$("#history-next").addEventListener("click", () => { if (state.historyMore) { state.historyOffset += pageSize; loadHistory(); } });
$("#history-show-all").addEventListener("click", () => {
  state.refFocusRequest++;
  state.historyFocus = null;
  state.historyOffset = 0;
  text("#history-view-label", "All archived commits");
  $("#history-show-all").hidden = true;
  clear($("#history-focus-tips"));
  loadHistory();
});
$("#repo-change-kind").addEventListener("change", () => { state.repoChangeOffset = 0; loadRepoChanges(); });
$("#repo-change-prev").addEventListener("click", () => { state.repoChangeOffset = Math.max(0, state.repoChangeOffset - pageSize); loadRepoChanges(); });
$("#repo-change-next").addEventListener("click", () => { state.repoChangeOffset += pageSize; loadRepoChanges(); });
$("#repo-bundle").addEventListener("click", () => { if (state.repo) bundle(state.repo); });
$("#snapshot-download").addEventListener("click", () => {
  if (state.repo && state.selectedSHA) window.location.assign("/api/repository/snapshot?" + query(repoQuery({ sha: state.selectedSHA })));
});
$("#commit-bundle-download").addEventListener("click", () => {
  if (state.repo && state.selectedSHA) window.location.assign("/api/repository/commit-bundle?" + query(repoQuery({ sha: state.selectedSHA })));
});
let userSearchTimer;
$("#user-search").addEventListener("input", event => {
  cancelUserSearch(true);
  if (!event.isComposing && event.currentTarget.value.trim().length >= 2) {
    userSearchTimer = setTimeout(searchUsers, searchIdleMs);
  }
});
$("#user-search-button").addEventListener("click", () => { clearTimeout(userSearchTimer); searchUsers(); });
$("#user-search").addEventListener("keydown", event => {
  if (event.isComposing) return;
  if (event.key === "ArrowDown" && asArray(state.searchResults).length) { event.preventDefault(); moveSearchSelection(1); }
  else if (event.key === "ArrowUp" && asArray(state.searchResults).length) { event.preventDefault(); moveSearchSelection(-1); }
  else if (event.key === "Escape") { event.preventDefault(); cancelUserSearch(true); }
  else if (event.key === "Enter") {
    event.preventDefault(); clearTimeout(userSearchTimer);
    if (state.searchIndex >= 0 && asArray(state.searchResults)[state.searchIndex]) selectSearchUser(state.searchResults[state.searchIndex]); else searchUsers();
  }
});
for (const button of $$('[data-export]')) button.addEventListener("click", () => {
  const values = button.dataset.export === "events" ? state.eventFilters : state.refFilters;
  window.location.assign("/api/export/" + button.dataset.export + "?" + query({ ...values, format: button.dataset.format }));
});
$("#repo-form").addEventListener("submit", async event => {
  event.preventDefault();
  const form = event.currentTarget, value = filters(form);
  const ok = await updateConfig(config => config.repositories.push({ owner: value.owner, name: value.name, poll_interval: value.poll_interval }), "Repository added.");
  if (ok) form.reset();
});
$("#user-form").addEventListener("submit", async event => {
  event.preventDefault();
  const form = event.currentTarget, value = filters(form);
  const user = { login: value.login, poll_interval: value.poll_interval, discover_repositories: form.elements.discover_repositories.checked, authenticated_events: form.elements.authenticated_events.checked, exclude_repositories: [] };
  const ok = await updateConfig(config => config.users.push(user), "Person added.");
  if (ok) form.reset();
});
$("#user-search").setAttribute("role", "combobox");
$("#user-search").setAttribute("aria-autocomplete", "list");
$("#user-search").setAttribute("aria-controls", "user-search-results");
$("#user-search").setAttribute("aria-expanded", "false");
$("#user-search-results").setAttribute("role", "listbox");
setupTargetControls();
new ResizeObserver(fitPeoplePage).observe($("#configured-users"));
setupRecentRefs();
setupOptionFilters();
$("#live-status").textContent = "View updates every 10 seconds";
$("#live-status").title = "The browser refreshes this view every 10 seconds. The archiver polls GitHub on its configured schedule.";
route();
if (location.hash && location.hash !== "#overview") loadStats();
async function liveRefresh() {
  if (document.hidden || state.refreshing) return;
  state.refreshing = true;
  const status = $("#live-status");
  try {
    const browsing = Boolean(document.querySelector("dialog[open]"));
    let ok = true;
    if (state.route === "overview" && !browsing) ok = await loadOverview(true);
    else {
      ok = await loadStats(true);
      if (state.route === "repository" && state.historyOffset === 0) ok = (await loadHistory(true)) && ok;
      if (state.route === "repository" && state.repoChangeOffset === 0) ok = (await loadRepoChanges(true)) && ok;
      if (!browsing) {
        if (state.route === "activity") ok = (await loadEvents(true)) && ok;
        if (state.route === "refs") ok = (await loadRefs(true)) && ok;
        if (state.route === "targets") ok = (await loadTargets(true)) && ok;
      }
    }
    status.textContent = ok ? "View refreshed " + new Intl.DateTimeFormat(undefined, { hour: "2-digit", minute: "2-digit", second: "2-digit" }).format(new Date()) : "View refresh failed; retrying";
    status.classList.toggle("error", !ok);
  } catch (_) { status.textContent = "Update failed; retrying"; status.classList.add("error"); }
  finally { state.refreshing = false; }
}
setInterval(liveRefresh, 10000);
document.addEventListener("visibilitychange", () => { if (!document.hidden) liveRefresh(); });
let resizeTimer;
window.addEventListener("resize", () => {
  clearTimeout(resizeTimer);
  resizeTimer = setTimeout(() => { if (state.route === "repository" && state.historyCommits) drawGraph(state.historyCommits, $("#commit-list")); }, 100);
});
