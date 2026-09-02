package logstream

var Jump = jump

func (l *Log) LockChannel() chan struct{} {
	return l.ch
}
