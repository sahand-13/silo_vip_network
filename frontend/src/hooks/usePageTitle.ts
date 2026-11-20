import { useEffect } from 'react';
import { useLocation } from 'react-router';
import { useTranslation } from 'react-i18next';

import { PANEL_TITLE_KEYS } from '@/routes/panelRoutes';
import { PANEL_BRAND } from '@/brand';

export function usePageTitle() {
  const { pathname } = useLocation();
  const { t } = useTranslation();

  useEffect(() => {
    const key = PANEL_TITLE_KEYS[pathname];
    document.title = key ? `${t(key)} | ${PANEL_BRAND}` : PANEL_BRAND;
  }, [pathname, t]);
}
