// Copyright 2012-2013 Tamás Gulácsi
// See LICENSE.txt
// Translated from cx_Oracle ((c) Anthony Tuininga) by Tamás Gulácsi
package goracle

/*
#cgo CFLAGS: -I/usr/include/oracle/11.2/client64
#cgo LDFLAGS: -lclntsh -L/usr/lib/oracle/11.2/client64/lib

#include <oci.h>
//#include <datetime.h>
//#include <structmember.h>
//#include <time.h>
//#include <oci.h>
//#include <orid.h>
//#include <xa.h>

*/
import "C"

import (
	"fmt"
	"sync"
	"unsafe"
)

// MakeDSN makea a data source name given the host port and SID.
func MakeDSN(host string, port int, sid, serviceName string) string {
	var format, conn string
	if sid != "" {
		conn = sid
		format = ("(DESCRIPTION=(ADDRESS_LIST=(ADDRESS=" +
			"(PROTOCOL=TCP)(HOST=%s)(PORT=%d)))(CONNECT_DATA=(SID=%s)))")
	} else {
		conn = serviceName
		format = ("(DESCRIPTION=(ADDRESS_LIST=(ADDRESS=" +
			"(PROTOCOL=TCP)(HOST=%s)(PORT=%d)))(CONNECT_DATA=" +
			"(SERVICE_NAME=%s)))")
	}
	if format == "" {
		return ""
	}
	return fmt.Sprintf(format, host, port, conn)
}

// ClientVersion returns the client's version (slice of 5 int32s)
func ClientVersion() []int32 {
	var majorVersion, minorVersion, updateNum, patchNum, portUpdateNum C.sword

	C.OCIClientVersion(&majorVersion, &minorVersion, &updateNum,
		&patchNum, &portUpdateNum)
	return []int32{int32(majorVersion), int32(minorVersion), int32(updateNum),
		int32(patchNum), int32(portUpdateNum)}
}

type Connection struct {
	handle        *C.OCISvcCtx  //connection
	serverHandle  *C.OCIServer  //server's handle
	sessionHandle *C.OCISession //session's handle
	environment   *Environment  //environment
	// sessionPool *SessionPool //sessionpool
	username, password, dsn, version string
	commitMode                       int64
	autocommit, release, attached    bool
	srvMtx                           sync.Mutex
}

// Connection_IsConnected()
//   Determines if the connection object is connected to the database.
func (conn Connection) IsConnected() bool {
	return conn.handle != nil
}

func (conn *Connection) AttrSet(key C.ub4, value unsafe.Pointer) *Error {
	return conn.environment.AttrSet(
		unsafe.Pointer(conn.handle), C.OCI_HTYPE_SVCCTX,
		key, value)
}

func (conn *Connection) ServerAttrSet(key C.ub4, value unsafe.Pointer) *Error {
	return conn.environment.AttrSet(
		unsafe.Pointer(conn.serverHandle), C.OCI_HTYPE_SERVER,
		key, value)
}

func (conn *Connection) SessionAttrSet(key C.ub4, value unsafe.Pointer) *Error {
	return conn.environment.AttrSet(
		unsafe.Pointer(conn.sessionHandle), C.OCI_HTYPE_SESSION,
		key, value)
}

//   Create a new connection object by connecting to the database.
func (conn *Connection) Connect(mode int64, twophase bool /*, newPassword string*/) error {
	credentialType := C.OCI_CRED_EXT
	var (
		status C.sword
		err    *Error
	)

	// allocate the server handle
	if ociHandleAlloc(unsafe.Pointer(conn.environment.handle),
		C.OCI_HTYPE_SERVER,
		(*unsafe.Pointer)(unsafe.Pointer(&conn.serverHandle))); err != nil {
		err.At = "Connect[allocate server handle]"
		return err
	}

	// attach to the server
	/*
	   if (cxBuffer_FromObject(&buffer, self->dsn,
	           self->environment->encoding) < 0)
	       return -1;
	*/

	buffer := make([]byte, len(conn.dsn)+1, max(16, len(conn.dsn), len(conn.username), len(conn.password))+1)
	copy(buffer, []byte(conn.dsn))
	buffer[len(conn.dsn)] = 0
	// dsn := C.CString(conn.dsn)
	// defer C.free(unsafe.Pointer(dsn))
	// Py_BEGIN_ALLOW_THREADS
	conn.srvMtx.Lock()
	status = C.OCIServerAttach(conn.serverHandle,
		conn.environment.errorHandle, (*C.OraText)(&buffer[0]),
		C.sb4(len(buffer)), C.OCI_DEFAULT)
	// Py_END_ALLOW_THREADS
	conn.srvMtx.Unlock()
	// cxBuffer_Clear(&buffer);
	if err = CheckStatus(status); err != nil {
		err.At = "Connect[server attach]"
		return err
	}

	// allocate the service context handle
	if err = ociHandleAlloc(unsafe.Pointer(conn.environment.handle),
		C.OCI_HTYPE_SVCCTX, (*unsafe.Pointer)(unsafe.Pointer(&conn.handle))); err != nil {
		err.At = "Connect[allocate service context handle]"
		return err
	}

	// set attribute for server handle
	if err = conn.AttrSet(C.OCI_ATTR_SERVER, unsafe.Pointer(conn.serverHandle)); err != nil {
		err.At = "Connect[set server handle]"
		return err
	}

	// set the internal and external names; these are needed for global
	// transactions but are limited in terms of the lengths of the strings
	if twophase {
		copy(buffer, []byte("goracle"))
		buffer[len("goracle")] = 0

		if err = conn.ServerAttrSet(C.OCI_ATTR_INTERNAL_NAME,
			unsafe.Pointer(&buffer[0])); err != nil {
			err.At = "Connect[set internal name]"
			return err
		}
		if err = conn.ServerAttrSet(C.OCI_ATTR_EXTERNAL_NAME,
			unsafe.Pointer(&buffer[0])); err != nil {
			err.At = "Connect[set external name]"
			return err
		}
	}

	// allocate the session handle
	if err = ociHandleAlloc(unsafe.Pointer(conn.environment.handle),
		C.OCI_HTYPE_SESSION,
		(*unsafe.Pointer)(unsafe.Pointer(&conn.sessionHandle))); err != nil {
		err.At = "Connect[allocate session handle]"
		return err
	}

	// set user name in session handle
	if conn.username != "" {
		copy(buffer, []byte(conn.username))
		buffer[len(conn.username)] = 0
		credentialType = C.OCI_CRED_RDBMS

		if err = conn.SessionAttrSet(C.OCI_ATTR_USERNAME,
			unsafe.Pointer(&buffer[0])); err != nil {
			err.At = "Connect[set user name]"
			return err
		}
	}

	// set password in session handle
	if conn.password != "" {
		copy(buffer, []byte(conn.password))
		buffer[len(conn.password)] = 0
		credentialType = C.OCI_CRED_RDBMS
		if err = conn.SessionAttrSet(C.OCI_ATTR_PASSWORD,
			unsafe.Pointer(&buffer[0])); err != nil {
			err.At = "Connect[set password]"
			return err
		}
	}

	/*
	   #ifdef OCI_ATTR_DRIVER_NAME
	       status = OCIAttrSet(self->sessionHandle, OCI_HTYPE_SESSION,
	               (text*) DRIVER_NAME, strlen(DRIVER_NAME), OCI_ATTR_DRIVER_NAME,
	               self->environment->errorHandle);
	       if (Environment_CheckForError(self->environment, status,
	               "Connection_Connect(): set driver name") < 0)
	           return -1;

	   #endif
	*/

	// set the session handle on the service context handle
	if err = conn.AttrSet(C.OCI_ATTR_SESSION,
		unsafe.Pointer(conn.sessionHandle)); err != nil {
		err.At = "Connect[set session handle]"
		return err
	}

	/*
	   // if a new password has been specified, change it which will also
	   // establish the session
	   if (newPasswordObj)
	       return Connection_ChangePassword(self, self->password, newPasswordObj);
	*/

	// begin the session
	// Py_BEGIN_ALLOW_THREADS
	conn.srvMtx.Lock()
	status = C.OCISessionBegin(conn.handle, conn.environment.errorHandle,
		conn.sessionHandle, C.ub4(credentialType), C.ub4(mode))
	// Py_END_ALLOW_THREADS
	conn.srvMtx.Unlock()
	if err = CheckStatus(status); err != nil {
		err.At = "Connect[begin session]"
		conn.sessionHandle = nil
		return err
	}

	return nil
}

func (conn *Connection) Rollback() {
	C.OCITransRollback(conn.handle, conn.environment.errorHandle,
		C.OCI_DEFAULT)
}

// Deallocate the connection, disconnecting from the database if necessary.
func (conn *Connection) Close() {
	if conn.release {
		// Py_BEGIN_ALLOW_THREADS
		conn.srvMtx.Lock()
		conn.Rollback()
		C.OCISessionRelease(conn.handle, conn.environment.errorHandle, nil,
			0, C.OCI_DEFAULT)
		// Py_END_ALLOW_THREADS
		conn.srvMtx.Unlock()
	} else if !conn.attached {
		if conn.sessionHandle != nil {
			// Py_BEGIN_ALLOW_THREADS
			conn.srvMtx.Lock()
			conn.Rollback()
			C.OCISessionEnd(conn.handle, conn.environment.errorHandle,
				conn.sessionHandle, C.OCI_DEFAULT)
			// Py_END_ALLOW_THREADS
			conn.srvMtx.Unlock()
		}
		if conn.serverHandle != nil {
			C.OCIServerDetach(conn.serverHandle,
				conn.environment.errorHandle, C.OCI_DEFAULT)
		}
	}
}

func max(numbers ...int) int {
	if len(numbers) == 0 {
		return 0
	}
	m := numbers[0]
	for _, x := range numbers {
		if m < x {
			m = x
		}
	}
	return m
}
