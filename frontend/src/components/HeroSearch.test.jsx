import { describe, it, expect, vi } from 'vitest';
import { render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import HeroSearch from './HeroSearch';

function setup(props = {}) {
  const onSearch = vi.fn();
  const user = userEvent.setup();
  render(<HeroSearch onSearch={onSearch} loading={false} {...props} />);
  const input = screen.getByRole('textbox', { name: /research question/i });
  const submit = screen.queryByRole('button', { name: /search evidence/i });
  return { onSearch, user, input, submit };
}

describe('HeroSearch', () => {
  it('submits the trimmed question', async () => {
    const { onSearch, user, input, submit } = setup();
    await user.clear(input);
    await user.type(input, '   How is retrieval verified?   ');
    await user.click(submit);
    expect(onSearch).toHaveBeenCalledTimes(1);
    expect(onSearch).toHaveBeenCalledWith('How is retrieval verified?');
  });

  it('submits with the Enter key', async () => {
    const { onSearch, user, input } = setup();
    await user.clear(input);
    await user.type(input, 'hybrid search{Enter}');
    expect(onSearch).toHaveBeenCalledWith('hybrid search');
  });

  it('does not submit an empty question', async () => {
    const { onSearch, user, input, submit } = setup();
    await user.clear(input);
    await user.click(submit);
    expect(onSearch).not.toHaveBeenCalled();
  });

  it('does not submit a whitespace-only question', async () => {
    const { onSearch, user, input } = setup();
    await user.clear(input);
    await user.type(input, '    {Enter}');
    expect(onSearch).not.toHaveBeenCalled();
  });

  it('disables the submit button while loading', () => {
    setup({ loading: true });
    expect(screen.getByRole('button', { name: /synthesizing/i })).toBeDisabled();
  });
});
