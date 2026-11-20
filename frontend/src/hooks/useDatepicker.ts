import { useEffect, useState } from 'react';
import { HttpUtil } from '@/utils';
import { parseMsg } from '@/utils/zodValidate';
import { DefaultsPayloadSchema } from '@/schemas/defaults';
import { usePanelRole } from '@/routes/panelAccess';

type Calendar = 'gregorian' | 'jalalian';

let cachedValue: Calendar = 'gregorian';
let fetched = false;
let pending: Promise<void> | null = null;
const listeners = new Set<(value: Calendar) => void>();

function notify(value: Calendar) {
  listeners.forEach((fn) => fn(value));
}

async function loadOnce(path: string): Promise<void> {
  if (fetched) return;
  if (pending) {
    await pending;
    return;
  }
  pending = (async () => {
    try {
      const msg = await HttpUtil.post(path);
      if (msg?.success) {
        const validated = parseMsg(msg, DefaultsPayloadSchema, 'setting/defaultSettings');
        cachedValue = validated.obj?.datepicker || 'gregorian';
        notify(cachedValue);
      }
    } finally {
      fetched = true;
      pending = null;
    }
  })();
  await pending;
}

export function setDatepicker(value: Calendar) {
  fetched = true;
  cachedValue = value || 'gregorian';
  notify(cachedValue);
}

export function useDatepicker() {
  const [datepicker, setLocal] = useState<Calendar>(cachedValue);
  const role = usePanelRole();

  useEffect(() => {
    listeners.add(setLocal);
    void loadOnce(role === 'owner' ? '/panel/api/setting/defaultSettings' : '/panel/api/delegation/client-page/defaults');
    return () => {
      listeners.delete(setLocal);
    };
  }, [role]);

  return { datepicker };
}
