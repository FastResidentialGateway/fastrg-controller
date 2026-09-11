import { createTheme } from '@mui/material/styles'

const MONO = '"JetBrains Mono", ui-monospace, SFMono-Regular, Menlo, monospace'
const SANS = '"Inter", "Noto Sans TC", system-ui, -apple-system, sans-serif'

// Operations-console palette: near-black ground, one cold-cyan accent, and
// semantic colors reserved for status dots and error text.
const dark = {
  primary: { main: '#4FB3BF', light: '#7FCBD4', dark: '#2F8994', contrastText: '#06171A' },
  secondary: { main: '#8B979C', contrastText: '#0B0F12' },
  success: { main: '#3FB27F' },
  warning: { main: '#D4A24C' },
  error: { main: '#E0645C' },
  info: { main: '#4FB3BF' },
  background: { default: '#0B0F12', paper: '#111619' },
  text: { primary: '#DCE3E6', secondary: '#8B979C', disabled: '#5A6569' },
  divider: 'rgba(255, 255, 255, 0.08)',
  action: { hover: 'rgba(255, 255, 255, 0.035)', selected: 'rgba(79, 179, 191, 0.10)' },
}

const light = {
  primary: { main: '#1E7C88', light: '#3E9BA6', dark: '#125A64', contrastText: '#FFFFFF' },
  secondary: { main: '#5B686C', contrastText: '#FFFFFF' },
  success: { main: '#1F7A52' },
  warning: { main: '#9A6B18' },
  error: { main: '#B23A34' },
  info: { main: '#1E7C88' },
  background: { default: '#F4F6F6', paper: '#FFFFFF' },
  text: { primary: '#161C1F', secondary: '#5B686C', disabled: '#98A3A6' },
  divider: 'rgba(0, 0, 0, 0.10)',
  action: { hover: 'rgba(0, 0, 0, 0.03)', selected: 'rgba(30, 124, 136, 0.08)' },
}

const theme = createTheme({
  cssVariables: { colorSchemeSelector: 'class' },
  colorSchemes: { dark: { palette: dark }, light: { palette: light } },
  shape: { borderRadius: 2 },
  // Separation comes from hairlines, never from elevation.
  shadows: Array(25).fill('none'),
  typography: {
    fontFamily: SANS,
    fontSize: 13,
    htmlFontSize: 16,
    body1: { fontSize: 13, lineHeight: 1.55 },
    body2: { fontSize: 13, lineHeight: 1.5 },
    caption: { fontSize: 11, lineHeight: 1.45 },
    subtitle1: { fontSize: 13, fontWeight: 600 },
    subtitle2: { fontSize: 12, fontWeight: 600 },
    h4: { fontSize: 17, fontWeight: 600, letterSpacing: '-0.01em' },
    h5: { fontSize: 15, fontWeight: 600, letterSpacing: '-0.01em' },
    h6: { fontSize: 13.5, fontWeight: 600 },
    button: { fontSize: 13, fontWeight: 500, textTransform: 'none', letterSpacing: 0 },
    overline: { fontSize: 11, fontWeight: 600, letterSpacing: '0.04em', textTransform: 'none' },
  },
  components: {
    MuiCssBaseline: {
      styleOverrides: {
        body: { WebkitFontSmoothing: 'antialiased' },
        // Identifiers — UUID, IP, MAC, VLAN, version, error code, timestamp.
        'code, samp, kbd, .mono': {
          fontFamily: MONO,
          fontSize: '12.5px',
          fontVariantNumeric: 'tabular-nums',
          letterSpacing: 0,
        },
      },
    },
    MuiPaper: {
      defaultProps: { variant: 'outlined' },
      styleOverrides: {
        root: ({ theme }) => ({
          backgroundImage: 'none',
          borderColor: theme.vars.palette.divider,
        }),
      },
    },
    MuiButton: {
      defaultProps: { disableElevation: true, size: 'small' },
      styleOverrides: {
        root: { paddingInline: 12, minHeight: 30 },
        outlined: ({ theme }) => ({ borderColor: theme.vars.palette.divider }),
      },
    },
    MuiIconButton: { defaultProps: { size: 'small' }, styleOverrides: { root: { borderRadius: 2 } } },
    MuiTextField: { defaultProps: { size: 'small', variant: 'outlined' } },
    MuiOutlinedInput: {
      styleOverrides: {
        root: { borderRadius: 2 },
        input: { fontSize: 13 },
        notchedOutline: ({ theme }) => ({ borderColor: theme.vars.palette.divider }),
      },
    },
    MuiInputLabel: { styleOverrides: { root: { fontSize: 13 } } },
    MuiMenuItem: { styleOverrides: { root: { fontSize: 13, minHeight: 32 } } },
    MuiCheckbox: { defaultProps: { size: 'small' } },
    MuiSwitch: { defaultProps: { size: 'small' } },
    MuiTable: { defaultProps: { size: 'small' } },
    MuiTableCell: {
      styleOverrides: {
        root: ({ theme }) => ({
          fontSize: 12.5,
          padding: '6px 12px',
          borderColor: theme.vars.palette.divider,
        }),
        head: ({ theme }) => ({
          fontSize: 10.5,
          fontWeight: 600,
          textTransform: 'uppercase',
          letterSpacing: '0.06em',
          whiteSpace: 'nowrap',
          color: theme.vars.palette.text.secondary,
          backgroundColor: theme.vars.palette.background.paper,
        }),
      },
    },
    // Status is shown as a dot plus text; chips remain only on the HSI tabs
    // until those are reworked, so they at least keep the square geometry.
    MuiChip: { defaultProps: { size: 'small' }, styleOverrides: { root: { borderRadius: 2, fontWeight: 500 } } },
    MuiAlert: {
      defaultProps: { variant: 'outlined' },
      styleOverrides: { root: { borderRadius: 2, fontSize: 12.5 } },
    },
    MuiDialog: {
      styleOverrides: {
        paper: ({ theme }) => ({ borderRadius: 2, border: `1px solid ${theme.vars.palette.divider}` }),
      },
    },
    MuiDialogTitle: { styleOverrides: { root: { fontSize: 14, fontWeight: 600, paddingBottom: 8 } } },
    MuiDialogContentText: { styleOverrides: { root: { fontSize: 13 } } },
    MuiMenu: {
      styleOverrides: {
        paper: ({ theme }) => ({ border: `1px solid ${theme.vars.palette.divider}` }),
      },
    },
    MuiTooltip: {
      styleOverrides: {
        tooltip: ({ theme }) => ({
          fontSize: 11.5,
          fontWeight: 500,
          borderRadius: 2,
          backgroundColor: theme.vars.palette.background.paper,
          color: theme.vars.palette.text.primary,
          border: `1px solid ${theme.vars.palette.divider}`,
        }),
      },
    },
    MuiTabs: {
      styleOverrides: {
        root: { minHeight: 36 },
        indicator: { height: 2 },
      },
    },
    MuiTab: {
      styleOverrides: {
        root: { minHeight: 36, padding: '8px 14px', fontSize: 13, textTransform: 'none' },
      },
    },
    MuiFormControlLabel: { styleOverrides: { label: { fontSize: 13 } } },
    MuiLinearProgress: { styleOverrides: { root: { borderRadius: 0 } } },
  },
})

export default theme
