import React from 'react'
import Alert from '@mui/material/Alert'
import AlertTitle from '@mui/material/AlertTitle'
import Box from '@mui/material/Box'

export default class ErrorBoundary extends React.Component {
  constructor(props){
    super(props)
    this.state = { hasError: false, error: null, info: null }
  }

  static getDerivedStateFromError(error){
    return { hasError: true, error }
  }

  componentDidCatch(error, info){
    this.setState({ error, info })
  }

  render(){
    if(this.state.hasError){
      const errMsg = (this.state.error && this.state.error.toString()) || 'Unknown error'
      const stack = this.state.info && this.state.info.componentStack
      return (
        <Box sx={{ p: 3 }}>
          <Alert severity="error">
            <AlertTitle>Something went wrong</AlertTitle>
            <Box component="pre" sx={{ m: 0, fontSize: 12, whiteSpace: 'pre-wrap' }}>{errMsg}</Box>
            {stack && (
              <Box component="pre" sx={{ mt: 1, mb: 0, fontSize: 12, whiteSpace: 'pre-wrap', color: 'text.secondary' }}>
                {stack}
              </Box>
            )}
          </Alert>
        </Box>
      )
    }
    return this.props.children
  }
}
