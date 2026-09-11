import React, { useEffect, useState } from 'react'
import { createPortal } from 'react-dom'
import { Link as RouterLink, useLocation } from 'react-router-dom'
import Box from '@mui/material/Box'
import IconButton from '@mui/material/IconButton'
import ListItemText from '@mui/material/ListItemText'
import Menu from '@mui/material/Menu'
import MenuItem from '@mui/material/MenuItem'
import Stack from '@mui/material/Stack'
import Tooltip from '@mui/material/Tooltip'
import Typography from '@mui/material/Typography'
import DarkModeOutlinedIcon from '@mui/icons-material/DarkModeOutlined'
import HubOutlinedIcon from '@mui/icons-material/HubOutlined'
import LanOutlinedIcon from '@mui/icons-material/LanOutlined'
import LightModeOutlinedIcon from '@mui/icons-material/LightModeOutlined'
import CheckOutlinedIcon from '@mui/icons-material/CheckOutlined'
import LogoutOutlinedIcon from '@mui/icons-material/LogoutOutlined'
import ReportGmailerrorredOutlinedIcon from '@mui/icons-material/ReportGmailerrorredOutlined'
import TranslateOutlinedIcon from '@mui/icons-material/TranslateOutlined'
import { useColorScheme } from '@mui/material/styles'
import { useI18n } from '../i18n/I18nContext'

const RAIL_WIDTH = 56
const HEADER_HEIGHT = 44
const ACTIONS_SLOT_ID = 'page-actions'

const NAV = [
  { to: '/nodes', labelKey: 'nav.nodes', Icon: LanOutlinedIcon },
  { to: '/failed-events', labelKey: 'nav.failedEvents', Icon: ReportGmailerrorredOutlinedIcon },
]

// Renders a page's own buttons into the header row.
export function PageActions({ children }) {
  const [slot, setSlot] = useState(null)
  useEffect(() => setSlot(document.getElementById(ACTIONS_SLOT_ID)), [])
  return slot ? createPortal(children, slot) : null
}

function RailButton({ to, label, Icon, active }) {
  return (
    <Tooltip title={label} placement="right">
      <IconButton
        component={RouterLink}
        to={to}
        aria-label={label}
        sx={{
          width: RAIL_WIDTH,
          height: 44,
          borderRadius: 0,
          color: active ? 'primary.main' : 'text.secondary',
          borderLeft: 2,
          borderColor: active ? 'primary.main' : 'transparent',
          '&:hover': { color: active ? 'primary.main' : 'text.primary' },
        }}
      >
        <Icon sx={{ fontSize: 19 }} />
      </IconButton>
    </Tooltip>
  )
}

function RailAction({ label, onClick, children }) {
  return (
    <Tooltip title={label} placement="right">
      <IconButton
        onClick={onClick}
        aria-label={label}
        sx={{ width: RAIL_WIDTH, height: 40, borderRadius: 0, color: 'text.secondary', '&:hover': { color: 'text.primary' } }}
      >
        {children}
      </IconButton>
    </Tooltip>
  )
}

// Every language in the table gets a row; the current one carries the check.
function LanguageAction() {
  const { language, languages, setLanguage } = useI18n()
  const [anchor, setAnchor] = useState(null)
  const current = languages.find(lang => lang.code === language)

  return (
    <>
      <RailAction label={current ? current.label : language} onClick={(e) => setAnchor(e.currentTarget)}>
        <TranslateOutlinedIcon sx={{ fontSize: 18 }} />
      </RailAction>
      <Menu
        open={Boolean(anchor)}
        anchorEl={anchor}
        onClose={() => setAnchor(null)}
        anchorOrigin={{ vertical: 'center', horizontal: 'right' }}
        transformOrigin={{ vertical: 'center', horizontal: 'left' }}
      >
        {languages.map(lang => (
          <MenuItem
            key={lang.code}
            selected={lang.code === language}
            onClick={() => { setLanguage(lang.code); setAnchor(null) }}
            sx={{ gap: 1.5, minWidth: 160 }}
          >
            <Box sx={{ width: 16, display: 'flex', alignItems: 'center' }}>
              {lang.code === language && <CheckOutlinedIcon sx={{ fontSize: 15, color: 'primary.main' }} />}
            </Box>
            <ListItemText primaryTypographyProps={{ fontSize: 13 }}>{lang.label}</ListItemText>
          </MenuItem>
        ))}
      </Menu>
    </>
  )
}

function ColorModeAction() {
  const { t } = useI18n()
  const { mode, systemMode, setMode } = useColorScheme()
  if (!mode) return <Box sx={{ height: 40 }} />

  const resolved = mode === 'system' ? (systemMode || 'dark') : mode
  const label = resolved === 'dark' ? t('nav.themeLight') : t('nav.themeDark')
  return (
    <RailAction label={label} onClick={() => setMode(resolved === 'dark' ? 'light' : 'dark')}>
      {resolved === 'dark' ? <LightModeOutlinedIcon sx={{ fontSize: 18 }} /> : <DarkModeOutlinedIcon sx={{ fontSize: 18 }} />}
    </RailAction>
  )
}

// Breadcrumb reads the path; the last crumb is the page name.
function useCrumbs() {
  const { pathname } = useLocation()
  const { t } = useI18n()
  const segments = pathname.split('/').filter(Boolean)
  if (segments.length === 0) return { crumbs: [], title: '' }

  const named = { nodes: t('nav.nodes'), 'failed-events': t('nav.failedEvents'), hsi: t('hsi.title') }
  const labels = segments.map(seg => ({ text: named[seg] || seg, mono: !named[seg] }))
  return { crumbs: labels.slice(0, -1), title: labels[labels.length - 1].text }
}

export default function AppShell({ isAuthenticated, onLogout, children }) {
  const { t } = useI18n()
  const { pathname } = useLocation()
  const { crumbs, title } = useCrumbs()

  // The login screen is full-bleed: no rail, no header.
  if (!isAuthenticated) {
    return <Box sx={{ minHeight: '100vh', bgcolor: 'background.default' }}>{children}</Box>
  }

  return (
    <Box sx={{ display: 'flex', minHeight: '100vh', bgcolor: 'background.default' }}>
      <Box
        component="nav"
        sx={{
          width: RAIL_WIDTH,
          flexShrink: 0,
          position: 'sticky',
          top: 0,
          alignSelf: 'flex-start',
          height: '100vh',
          display: 'flex',
          flexDirection: 'column',
          bgcolor: 'background.paper',
          borderRight: 1,
          borderColor: 'divider',
        }}
      >
        <Box sx={{ height: HEADER_HEIGHT, display: 'flex', alignItems: 'center', justifyContent: 'center', borderBottom: 1, borderColor: 'divider' }}>
          <HubOutlinedIcon sx={{ fontSize: 19, color: 'primary.main' }} />
        </Box>
        <Stack sx={{ pt: 1 }}>
          {NAV.map(item => (
            <RailButton
              key={item.to}
              to={item.to}
              label={t(item.labelKey)}
              Icon={item.Icon}
              active={pathname === item.to || pathname.startsWith(`${item.to}/`)}
            />
          ))}
        </Stack>
        <Box sx={{ flexGrow: 1 }} />
        <Stack sx={{ pb: 1 }}>
          <ColorModeAction />
          <LanguageAction />
          <RailAction label={t('nav.logout')} onClick={onLogout}>
            <LogoutOutlinedIcon sx={{ fontSize: 18 }} />
          </RailAction>
        </Stack>
      </Box>

      <Box sx={{ flexGrow: 1, minWidth: 0, display: 'flex', flexDirection: 'column' }}>
        <Stack
          direction="row"
          spacing={1}
          component="header"
          sx={{
            height: HEADER_HEIGHT,
            flexShrink: 0,
            alignItems: 'center',
            px: 3,
            borderBottom: 1,
            borderColor: 'divider',
            bgcolor: 'background.paper',
            position: 'sticky',
            top: 0,
            zIndex: 2,
          }}
        >
          <Typography variant="caption" color="text.secondary" noWrap component="div">
            fastrg&nbsp;/&nbsp;
            {crumbs.map((crumb, i) => (
              <React.Fragment key={i}>
                {crumb.mono ? <Box component="code" sx={{ color: 'text.secondary' }}>{crumb.text}</Box> : crumb.text}
                &nbsp;/&nbsp;
              </React.Fragment>
            ))}
          </Typography>
          <Typography variant="h6" component="h1" noWrap>{title}</Typography>
          <Box sx={{ flexGrow: 1 }} />
          <Stack id={ACTIONS_SLOT_ID} direction="row" spacing={1} sx={{ alignItems: 'center' }} />
        </Stack>

        <Box component="main" sx={{ flexGrow: 1, px: 3, py: 2.5, minWidth: 0 }}>
          {children}
        </Box>
      </Box>
    </Box>
  )
}
