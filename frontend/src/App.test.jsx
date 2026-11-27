import { afterEach, describe, expect, it, vi } from 'vitest';
import { act, render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import App from './App';
import {
  earlyRefusal, quotedAnswer, refusalA, refusalB, refusalC, synthesizedAnswer,
  supportedCitation, quotedClaims, synthesizedSkips, brokenPositional,
} from './test/fixtures';

// App-level regression tests for FE-3a (design docs/design/FE-3.md §Test plan, App.test.jsx rows).
// Toast is mocked so every message it was rendered with can be inspected; it renders nothing.
const { toastSpy } = vi.hoisted(() => ({ toastSpy: vi.fn() }));
vi.mock('./components/Toast', () => ({
  default: (props) => {
    toastSpy(props);
    return null;
  },
}));

const QUESTION = 'How do retrieval agents ground their answers?';
const QUOTED = /Hybrid search finds the passages/;
const SYNTHESIZED = /Each claim is verified against its cited passage/;
const WRITING = /Writing a summary from these passages/;

const ok = (data) => ({ ok: true, status: 200, json: async () => data });

// Answers /v1/ask by request body (quick: true vs full) and /v1/papers/discover separately. Each handler is a
// response object, or a function returning one (or a promise of one). Any other request, or a stage the test didn't
// provide, is recorded in `unexpected` and fails the test in afterEach.
let unexpected = [];
function stubFetch({ quick, full, discover } = {}) {
  const answer = (handler, label) => {
    if (handler === undefined) {
      unexpected.push(label);
      return Promise.reject(new Error(`unexpected request: ${label}`));
    }
    return Promise.resolve(typeof handler === 'function' ? handler() : handler).then(ok);
  };
  const fetchMock = vi.fn((url, init = {}) => {
    if (url === '/v1/ask') {
      const body = JSON.parse(init.body);
      return body.quick === true ? answer(quick, 'ask quick') : answer(full, 'ask full');
    }
    if (url === '/v1/papers/discover') return answer(discover, 'discover');
    unexpected.push(`${init.method || 'GET'} ${url}`);
    return Promise.reject(new Error(`unexpected request: ${url}`));
  });
  vi.stubGlobal('fetch', fetchMock);
  return fetchMock;
}

function deferred() {
  let resolve;
  const promise = new Promise((r) => {
    resolve = r;
  });
  return { promise, resolve };
}

async function ask(user, question = QUESTION) {
  const input = screen.getByRole('textbox', { name: 'Research question' });
  await user.clear(input);
  await user.type(input, `${question}{Enter}`);
}

const refusalPanel = () => screen.findByRole('status', { name: /no answer shown/i });

function expectNoToastMessage() {
  expect(toastSpy).toHaveBeenCalled();
  for (const [props] of toastSpy.mock.calls) expect(props.message).toBe('');
}

afterEach(() => {
  vi.unstubAllGlobals();
  toastSpy.mockClear();
  const seen = unexpected;
  unexpected = [];
  expect(seen).toEqual([]);
});

describe('App: refusal states (FE-3a)', () => {
  it('AC1: refusal A, the whole page never says "verified"', async () => {
    stubFetch({ quick: refusalA });
    const user = userEvent.setup();
    render(<App />);
    await ask(user);
    await refusalPanel();
    expect(document.body.textContent).not.toMatch(/verified/i);
  });

  it('AC1: refusal C, the whole page never says "verified"', async () => {
    stubFetch({ quick: refusalC });
    const user = userEvent.setup();
    render(<App />);
    await ask(user);
    await refusalPanel();
    expect(document.body.textContent).not.toMatch(/verified/i);
  });

  it('AC2: refusal B is calm: no toast, no alert, passages visible, no writing stage', async () => {
    const fetchMock = stubFetch({ quick: refusalB });
    const user = userEvent.setup();
    render(<App />);
    await ask(user);
    await refusalPanel();
    // Let any follow-up request or toast a broken App would make settle before asserting their absence.
    await act(async () => {
      await new Promise((r) => setTimeout(r, 0));
    });
    expectNoToastMessage();
    expect(screen.queryByRole('alert')).toBeNull();
    expect(screen.getByRole('heading', { name: /Retrieved passages/ })).toBeVisible();
    expect(fetchMock).toHaveBeenCalledTimes(1);
  });

  it('AC3: early refusal renders as today and the discover button posts the question', async () => {
    const fetchMock = stubFetch({
      quick: earlyRefusal,
      discover: { title: 'A Newly Discovered Paper On Grounding', arxiv_id: '2405.00001' },
    });
    const user = userEvent.setup();
    render(<App />);
    await ask(user);

    expect(await screen.findByText('Relevance Protection Activated:')).toBeInTheDocument();
    const code = screen.getByText((_, el) => el.tagName === 'CODE' && el.textContent === `"${QUESTION}"`);
    expect(code).toBeInTheDocument();
    expect(screen.queryAllByRole('article')).toHaveLength(0);
    expect(screen.queryByText('Clinical Literature Synthesis')).toBeNull();

    await user.click(screen.getByRole('button', { name: `⚡ Search arXiv for "${QUESTION}" & Index` }));
    const discoverCalls = fetchMock.mock.calls.filter(([url]) => url === '/v1/papers/discover');
    expect(discoverCalls).toHaveLength(1);
    const [, init] = discoverCalls[0];
    expect(init.method).toBe('POST');
    expect(JSON.parse(init.body)).toEqual({ query: QUESTION });

    // The re-run search after indexing gets the early refusal again; still no cards, no synthesis card.
    await waitFor(() => expect(fetchMock.mock.calls.filter(([url]) => url === '/v1/ask')).toHaveLength(2));
    expect(await screen.findByText('Relevance Protection Activated:')).toBeInTheDocument();
    expect(screen.queryAllByRole('article')).toHaveLength(0);
    expect(screen.queryByText('Clinical Literature Synthesis')).toBeNull();
  });

  it('AC4: the panel link (Tab, then Enter) moves focus to the passages heading', async () => {
    stubFetch({ quick: refusalA });
    const user = userEvent.setup();
    render(<App />);
    await ask(user);
    await refusalPanel();

    const link = screen.getByRole('link', { name: 'Read the 2 retrieved passages' });
    for (let i = 0; i < 60 && document.activeElement !== link; i++) await user.tab();
    expect(document.activeElement).toBe(link);

    await user.keyboard('{Enter}');
    const heading = document.getElementById('references-heading');
    expect(heading).not.toBeNull();
    expect(document.activeElement).toBe(heading);
    expect(document.activeElement).toHaveAttribute('id', 'references-heading');
  });
});

describe('App: two-stage answer (FE-3a)', () => {
  it('AC5a: quoted answer with a writing status, then swapped for the synthesized one', async () => {
    const full = deferred();
    const fetchMock = stubFetch({ quick: quotedAnswer, full: () => full.promise });
    const user = userEvent.setup();
    render(<App />);
    await ask(user);

    expect(await screen.findByText(QUOTED)).toBeInTheDocument();
    expect(await screen.findByText(WRITING)).toBeInTheDocument();
    expect(screen.queryByText(SYNTHESIZED)).toBeNull();
    expect(fetchMock).toHaveBeenCalledTimes(2);

    await act(async () => {
      full.resolve(synthesizedAnswer);
    });
    expect(await screen.findByText(SYNTHESIZED)).toBeInTheDocument();
    expect(screen.queryByText(QUOTED)).toBeNull();
    expect(screen.queryByText(WRITING)).toBeNull();
  });

  it('AC5b: a refused written answer keeps the quoted one, no panel, no toast', async () => {
    const full = deferred();
    stubFetch({ quick: quotedAnswer, full: () => full.promise });
    const user = userEvent.setup();
    render(<App />);
    await ask(user);

    expect(await screen.findByText(QUOTED)).toBeInTheDocument();
    expect(await screen.findByText(WRITING)).toBeInTheDocument();

    await act(async () => {
      full.resolve(refusalC);
    });
    // The writing status clears in the same update that decides whether to swap.
    await waitFor(() => expect(screen.queryByText(WRITING)).toBeNull());
    expect(screen.getByText(QUOTED)).toBeInTheDocument();
    expect(screen.queryByText('No answer shown')).toBeNull();
    expect(screen.queryByRole('status', { name: /no answer shown/i })).toBeNull();
    expectNoToastMessage();
  });
});

// FE-4a (design docs/design/FE-4.md §4, test plan rows AC2, AC2b, AC2c).
const claimsList = () => screen.findByRole('list', { name: 'Claims in this answer' });
const citationCard = (marker) => screen.getByRole('article', { name: new RegExp(`^Citation \\[${marker}\\]`) });
const activeCards = () => document.querySelectorAll('[data-active]');

describe('App: claim markers focus their card (FE-4a)', () => {
  it('AC2/AC2c: clicking [3] focuses card [3] and marks it active; heading counts cards, footer counts checks', async () => {
    stubFetch({ quick: synthesizedSkips });
    const user = userEvent.setup();
    render(<App />);
    await ask(user);
    const list = await claimsList();

    expect(within(screen.getByRole('heading', { name: /^References/ })).getByText('2 cited')).toBeInTheDocument();
    expect(screen.getByText('3 of 3 citations supported')).toBeInTheDocument();
    expect(document.querySelectorAll('[id="ref-1"]')).toHaveLength(1);
    expect(activeCards()).toHaveLength(0);

    await user.click(within(list).getByRole('button', { name: '[3]' }));
    expect(document.activeElement).toBe(citationCard(3));
    expect(citationCard(3)).toHaveAttribute('data-active', 'true');
    expect(activeCards()).toHaveLength(1);
  });

  it('AC2: Tab to [1] opens its preview tooltip; Enter moves focus to card [1] and the tooltip closes', async () => {
    stubFetch({ quick: synthesizedSkips });
    const user = userEvent.setup();
    render(<App />);
    await ask(user);
    const list = await claimsList();

    const m1 = within(within(list).getAllByRole('listitem')[0]).getByRole('button', { name: '[1]' });
    for (let i = 0; i < 60 && document.activeElement !== m1; i++) await user.tab();
    expect(m1).toHaveFocus();
    const tip = await screen.findByRole('tooltip');
    expect(tip).toHaveTextContent(supportedCitation.title);
    expect(tip).toHaveTextContent('Supported · 87.3%');

    await user.keyboard('{Enter}');
    expect(document.activeElement).toBe(citationCard(1));
    expect(citationCard(1)).toHaveAttribute('data-active', 'true');
    await waitFor(() => expect(screen.queryByRole('tooltip')).toBeNull());
  });

  it('AC2: Space on a marker also focuses its card', async () => {
    stubFetch({ quick: synthesizedSkips });
    const user = userEvent.setup();
    render(<App />);
    await ask(user);
    const list = await claimsList();
    const m3 = within(list).getByRole('button', { name: '[3]' });
    for (let i = 0; i < 60 && document.activeElement !== m3; i++) await user.tab();
    await user.keyboard(' ');
    expect(document.activeElement).toBe(citationCard(3));
  });

  it('AC2: with a broken positional invariant, markers still map by number', async () => {
    stubFetch({ quick: brokenPositional });
    const user = userEvent.setup();
    render(<App />);
    await ask(user);
    const items = within(await claimsList()).getAllByRole('listitem');

    await user.click(within(items[1]).getByRole('button', { name: '[3]' }));
    expect(document.activeElement).toBe(citationCard(3));
    await user.click(within(items[0]).getByRole('button', { name: '[1]' }));
    expect(document.activeElement).toBe(citationCard(1));
    expect(activeCards()).toHaveLength(1);
  });

  it.each([
    ['quoted answer (prose)', { ...quotedAnswer, synthesis_available: false }],
    ['quoted claims', quotedClaims],
    ['refusal A', refusalA],
    ['refusal B', refusalB],
    ['refusal C', refusalC],
  ])('AC2b: %s: no card is active on load', async (_name, response) => {
    stubFetch({ quick: response });
    const user = userEvent.setup();
    render(<App />);
    await ask(user);
    await waitFor(() => expect(screen.getAllByRole('article').length).toBeGreaterThan(0));
    expect(activeCards()).toHaveLength(0);
  });

  it('AC2b: after the written answer replaces the quoted one, no card is active', async () => {
    const full = deferred();
    stubFetch({ quick: { ...quotedClaims, synthesis_available: true }, full: () => full.promise });
    const user = userEvent.setup();
    render(<App />);
    await ask(user);
    const list = await claimsList();
    await user.click(within(list).getByRole('button', { name: '[2]' }));
    expect(citationCard(2)).toHaveAttribute('data-active', 'true');

    await act(async () => {
      full.resolve(synthesizedSkips);
    });
    expect(await screen.findByText(synthesizedSkips.claims[0].text)).toBeInTheDocument();
    expect(activeCards()).toHaveLength(0);
  });
});
