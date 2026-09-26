// The "discard unsaved input?" guard every form modal shares.
//
// A modal has three easy exits — Esc, the backdrop, a close/cancel button — and
// each of them used to throw away whatever had been typed without a word. Every
// exit now goes through `requestClose`: a clean form closes at once, a dirty one
// raises a ConfirmDialog (spread `confirmProps` onto it) whose confirm closes
// and whose cancel returns to the form.
//
// "Dirty" is the caller's to compute, and always means the same thing: the
// input DIFFERS FROM THE VALUE THE FORM OPENED WITH (or was pre-filled with) —
// never merely "non-empty", or a pre-filled field would nag on an untouched
// form.
//
// The confirm state is cleared on both confirm and cancel, so a modal that
// stays mounted between openings (and only renders null while closed) never
// re-opens with the confirm already showing.

import { useCallback, useMemo, useState } from 'react';

export interface DiscardConfirmProps {
  open: boolean;
  onConfirm: () => void;
  onCancel: () => void;
}

export interface DiscardGuard {
  /** Route every close affordance (Esc, backdrop, ×, cancel) through this. */
  requestClose: () => void;
  /** True while the discard confirm is on screen. */
  confirming: boolean;
  /** Spread onto `<ConfirmDialog>` alongside its title/label/body. */
  confirmProps: DiscardConfirmProps;
}

export function useDiscardGuard(
  dirty: boolean,
  onClose: () => void,
  /** `disabled` swallows close requests outright (e.g. while a save is in flight). */
  { disabled = false }: { disabled?: boolean } = {},
): DiscardGuard {
  const [confirming, setConfirming] = useState(false);

  const requestClose = useCallback((): void => {
    if (disabled) return;
    if (dirty) {
      setConfirming(true);
      return;
    }
    onClose();
  }, [disabled, dirty, onClose]);

  const onConfirm = useCallback((): void => {
    setConfirming(false);
    onClose();
  }, [onClose]);

  const onCancel = useCallback((): void => setConfirming(false), []);

  return useMemo(
    () => ({ requestClose, confirming, confirmProps: { open: confirming, onConfirm, onCancel } }),
    [requestClose, confirming, onConfirm, onCancel],
  );
}
